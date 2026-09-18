package graph

import (
	"strings"
	"testing"
)

func TestLangForExt(t *testing.T) {
	cases := []struct{ ext, want string }{
		{".go", "golang"},
		{".py", "python"},
		{".ts", "typescript"},
		{".tsx", "tsx"},
		{".js", "javascript"},
		{".jsx", "javascript"},
		{".rs", "rust"},
		{".java", "java"},
		{".rb", "ruby"},
		{".xyz", ""},
	}
	for _, tc := range cases {
		if got := LangForExt(tc.ext); got != tc.want {
			t.Errorf("LangForExt(%q) = %q, want %q", tc.ext, got, tc.want)
		}
	}
}

func TestExtract_go(t *testing.T) {
	src := []byte(`
package foo

import (
	"fmt"
	"github.com/example/bar"
)

func ExportedFunc() {}
func unexportedFunc() {}

type ExportedType struct{}
type unexportedType struct{}
`)
	d, err := Extract(src, "golang")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(d.Imports, "fmt") {
		t.Errorf("missing import fmt, got: %v", d.Imports)
	}
	if !containsStr(d.Imports, "github.com/example/bar") {
		t.Errorf("missing import bar, got: %v", d.Imports)
	}
	if !containsStr(d.ExportFns, "ExportedFunc") {
		t.Errorf("missing ExportedFunc, got: %v", d.ExportFns)
	}
	if containsStr(d.ExportFns, "unexportedFunc") {
		t.Errorf("unexportedFunc should not appear in exports")
	}
	if !containsStr(d.ExportTypes, "ExportedType") {
		t.Errorf("missing ExportedType, got: %v", d.ExportTypes)
	}
	if containsStr(d.ExportTypes, "unexportedType") {
		t.Errorf("unexportedType should not appear in exports")
	}
}

func TestExtract_python(t *testing.T) {
	src := []byte(`
import os
import sys
from django.models import Model

def public_func():
    pass

def _private_func():
    pass

class PublicClass:
    pass
`)
	d, err := Extract(src, "python")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(d.Imports, "os") {
		t.Errorf("missing import os: %v", d.Imports)
	}
	if !containsStr(d.ExportFns, "public_func") {
		t.Errorf("missing public_func: %v", d.ExportFns)
	}
	if containsStr(d.ExportFns, "_private_func") {
		t.Errorf("_private_func should be excluded")
	}
	if !containsStr(d.ExportTypes, "PublicClass") {
		t.Errorf("missing PublicClass: %v", d.ExportTypes)
	}
}

func TestExtract_javascript(t *testing.T) {
	src := []byte(`
import { something } from 'some-module';
const lib = require('another-lib');

export function PublicFunc() {}
export class PublicClass {}

function privateFunc() {}
`)
	d, err := Extract(src, "javascript")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(d.Imports, "some-module") {
		t.Errorf("missing import some-module: %v", d.Imports)
	}
	if !containsStr(d.Imports, "another-lib") {
		t.Errorf("missing require another-lib: %v", d.Imports)
	}
	if !containsStr(d.ExportFns, "PublicFunc") {
		t.Errorf("missing PublicFunc: %v", d.ExportFns)
	}
	if !containsStr(d.ExportTypes, "PublicClass") {
		t.Errorf("missing PublicClass: %v", d.ExportTypes)
	}
	if containsStr(d.ExportFns, "privateFunc") {
		t.Errorf("privateFunc should not appear in exports")
	}
}

func TestExtract_unknownLang(t *testing.T) {
	d, err := Extract([]byte("anything"), "unknown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(d.Imports)+len(d.ExportFns)+len(d.ExportTypes) != 0 {
		t.Errorf("expected empty structural data for unknown language, got: %+v", d)
	}
}

func TestExtract_go_extended(t *testing.T) {
	src := []byte(`// Package foo does foo-ish things.
// Second line of package doc.
package foo

import (
	"fmt"
	"github.com/example/bar"
)

// Bar does the bar thing.
// Handles the bar case.
func Bar(x int, y string) (int, error) {
	return 0, nil
}

// Baz is a struct type.
type Baz struct {
	Field1 int
	Field2 string
}

// Val is exported.
const Val = 42

const (
	ExportedA = 1
	unexportedB = 2
)
`)
	d, err := Extract(src, "golang")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(d.PackageDoc, "Package foo does foo-ish things.") {
		t.Errorf("PackageDoc = %q, want it to contain package doc", d.PackageDoc)
	}
	if !strings.Contains(d.PackageDoc, "Second line") {
		t.Errorf("PackageDoc missing second line: %q", d.PackageDoc)
	}
	var bar *ExportedFunc
	for i := range d.ExportedFuncs {
		if d.ExportedFuncs[i].Name == "Bar" {
			bar = &d.ExportedFuncs[i]
		}
	}
	if bar == nil {
		t.Fatalf("Bar function not extracted: %+v", d.ExportedFuncs)
	}
	if !strings.Contains(bar.Signature, "func Bar(x int, y string)") {
		t.Errorf("Bar.Signature = %q", bar.Signature)
	}
	if !strings.Contains(bar.Doc, "Bar does the bar thing.") {
		t.Errorf("Bar.Doc = %q", bar.Doc)
	}
	var baz *ExportedType
	for i := range d.ExportedTypes {
		if d.ExportedTypes[i].Name == "Baz" {
			baz = &d.ExportedTypes[i]
		}
	}
	if baz == nil {
		t.Fatalf("Baz type not extracted: %+v", d.ExportedTypes)
	}
	if !strings.Contains(baz.Doc, "Baz is a struct type.") {
		t.Errorf("Baz.Doc = %q", baz.Doc)
	}
	if len(baz.Fields) != 2 {
		t.Errorf("Baz.Fields = %v, want 2", baz.Fields)
	}
	if !containsStr(d.Consts, "Val") {
		t.Errorf("missing const Val: %v", d.Consts)
	}
	if !containsStr(d.Consts, "ExportedA") {
		t.Errorf("missing const ExportedA: %v", d.Consts)
	}
	if containsStr(d.Consts, "unexportedB") {
		t.Errorf("unexportedB should not appear in consts")
	}
	// Backward-compat name slices must still be populated.
	if !containsStr(d.ExportFns, "Bar") {
		t.Errorf("ExportFns missing Bar: %v", d.ExportFns)
	}
	if !containsStr(d.ExportTypes, "Baz") {
		t.Errorf("ExportTypes missing Baz: %v", d.ExportTypes)
	}
	if !containsStr(d.Imports, "fmt") || !containsStr(d.Imports, "github.com/example/bar") {
		t.Errorf("imports = %v", d.Imports)
	}
}

func TestExtract_typescript_extended(t *testing.T) {
	src := []byte(`/**
 * Module doc.
 */
import { thing } from 'mod';
const util = require('other');

/** Doc for PublicFn */
export function PublicFn(x: number): string { return ""; }

/** Doc for MyClass */
export class MyClass {
  a: number = 0;
  b: string;
  greet() {}
}

/** Doc for MyIface */
export interface MyIface {
  x: number;
  y: string;
}

export const MY_CONST = 5;
`)
	d, err := Extract(src, "typescript")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(d.PackageDoc, "Module doc.") {
		t.Errorf("PackageDoc = %q", d.PackageDoc)
	}
	if !containsStr(d.Imports, "mod") {
		t.Errorf("missing import mod: %v", d.Imports)
	}
	if !containsStr(d.Imports, "other") {
		t.Errorf("missing require other: %v", d.Imports)
	}
	var pf *ExportedFunc
	for i := range d.ExportedFuncs {
		if d.ExportedFuncs[i].Name == "PublicFn" {
			pf = &d.ExportedFuncs[i]
		}
	}
	if pf == nil {
		t.Fatalf("PublicFn not found: %+v", d.ExportedFuncs)
	}
	if !strings.Contains(pf.Signature, "function PublicFn") {
		t.Errorf("PublicFn.Signature = %q", pf.Signature)
	}
	if !strings.Contains(pf.Doc, "Doc for PublicFn") {
		t.Errorf("PublicFn.Doc = %q", pf.Doc)
	}
	foundClass, foundIface := false, false
	for _, tp := range d.ExportedTypes {
		if tp.Name == "MyClass" {
			foundClass = true
			if !strings.Contains(tp.Doc, "Doc for MyClass") {
				t.Errorf("MyClass.Doc = %q", tp.Doc)
			}
			if len(tp.Fields) < 2 {
				t.Errorf("MyClass.Fields = %v", tp.Fields)
			}
		}
		if tp.Name == "MyIface" {
			foundIface = true
			if len(tp.Fields) != 2 {
				t.Errorf("MyIface.Fields = %v", tp.Fields)
			}
		}
	}
	if !foundClass || !foundIface {
		t.Errorf("classes = %+v", d.ExportedTypes)
	}
	if !containsStr(d.Consts, "MY_CONST") {
		t.Errorf("missing MY_CONST: %v", d.Consts)
	}
}

func TestExtract_java_extended(t *testing.T) {
	src := []byte(`package com.example.foo;

import com.example.bar.Bar;
import java.util.List;

/** Public doc for Foo */
public class Foo {
  public static final int MAX = 10;
  private String name;

  /** doc for greet */
  public String greet(String who) { return ""; }
}

/** Iface doc */
public interface MyIface {
  int getX();
}
`)
	d, err := Extract(src, "java")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(d.Imports, "com.example.bar.Bar") {
		t.Errorf("missing import: %v", d.Imports)
	}
	if !containsStr(d.Imports, "java.util.List") {
		t.Errorf("missing import: %v", d.Imports)
	}
	if !strings.Contains(d.PackageDoc, "Public doc for Foo") {
		t.Errorf("PackageDoc = %q", d.PackageDoc)
	}
	foundFoo, foundIface := false, false
	for _, tp := range d.ExportedTypes {
		if tp.Name == "Foo" {
			foundFoo = true
			if !strings.Contains(tp.Doc, "Public doc for Foo") {
				t.Errorf("Foo.Doc = %q", tp.Doc)
			}
			if len(tp.Fields) != 1 {
				t.Errorf("Foo.Fields should contain only public MAX, got %v", tp.Fields)
			}
		}
		if tp.Name == "MyIface" {
			foundIface = true
		}
	}
	if !foundFoo || !foundIface {
		t.Errorf("classes = %+v", d.ExportedTypes)
	}
	var greet *ExportedFunc
	for i := range d.ExportedFuncs {
		if d.ExportedFuncs[i].Name == "greet" {
			greet = &d.ExportedFuncs[i]
		}
	}
	if greet == nil {
		t.Fatalf("greet not found: %+v", d.ExportedFuncs)
	}
	if !strings.Contains(greet.Signature, "greet(String who)") {
		t.Errorf("greet.Signature = %q", greet.Signature)
	}
	if !strings.Contains(greet.Doc, "doc for greet") {
		t.Errorf("greet.Doc = %q", greet.Doc)
	}
}

func TestExtract_rust_extended(t *testing.T) {
	src := []byte(`//! Module-level doc line 1.
//! Module-level doc line 2.

use std::io;
use crate::foo::Bar;

/// Doc for pub_fn.
pub fn pub_fn(x: i32) -> i32 { x }

fn private_fn() {}

/// Doc for MyStruct.
pub struct MyStruct {
    pub field_a: i32,
    field_b: String,
}

/// Doc for MyTrait.
pub trait MyTrait {
    fn method(&self) -> i32;
}

pub const MAX: usize = 10;
`)
	d, err := Extract(src, "rust")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(d.PackageDoc, "Module-level doc line 1.") {
		t.Errorf("PackageDoc = %q", d.PackageDoc)
	}
	if !strings.Contains(d.PackageDoc, "line 2") {
		t.Errorf("PackageDoc missing second line: %q", d.PackageDoc)
	}
	if !containsStr(d.Imports, "std::io") {
		t.Errorf("imports = %v", d.Imports)
	}
	var pf *ExportedFunc
	for i := range d.ExportedFuncs {
		if d.ExportedFuncs[i].Name == "pub_fn" {
			pf = &d.ExportedFuncs[i]
		}
	}
	if pf == nil {
		t.Fatalf("pub_fn not found: %+v", d.ExportedFuncs)
	}
	if !strings.Contains(pf.Signature, "pub fn pub_fn(x: i32)") {
		t.Errorf("pub_fn.Signature = %q", pf.Signature)
	}
	if pf.Doc != "Doc for pub_fn." {
		t.Errorf("pub_fn.Doc = %q", pf.Doc)
	}
	for _, fn := range d.ExportedFuncs {
		if fn.Name == "private_fn" {
			t.Errorf("private_fn should not be exported")
		}
	}
	foundStruct, foundTrait := false, false
	for _, tp := range d.ExportedTypes {
		if tp.Name == "MyStruct" {
			foundStruct = true
			if len(tp.Fields) != 2 {
				t.Errorf("MyStruct.Fields = %v", tp.Fields)
			}
		}
		if tp.Name == "MyTrait" {
			foundTrait = true
			if len(tp.Fields) != 1 {
				t.Errorf("MyTrait method rows = %v", tp.Fields)
			}
		}
	}
	if !foundStruct || !foundTrait {
		t.Errorf("types = %+v", d.ExportedTypes)
	}
	if !containsStr(d.Consts, "MAX") {
		t.Errorf("MAX missing: %v", d.Consts)
	}
}

func TestExtract_ruby_extended(t *testing.T) {
	src := []byte(`# Module doc line 1.
# Module doc line 2.

require 'json'
require_relative 'foo'

MAX_SIZE = 100

# Doc for greet
def greet(name)
  "hi"
end

# Doc for MyClass
class MyClass
  attr_accessor :name
end

# Doc for MyModule
module MyModule
end
`)
	d, err := Extract(src, "ruby")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(d.PackageDoc, "Module doc line 1.") {
		t.Errorf("PackageDoc = %q", d.PackageDoc)
	}
	if !containsStr(d.Imports, "json") {
		t.Errorf("missing require json: %v", d.Imports)
	}
	if !containsStr(d.Imports, "foo") {
		t.Errorf("missing require_relative foo: %v", d.Imports)
	}
	if !containsStr(d.Consts, "MAX_SIZE") {
		t.Errorf("missing MAX_SIZE: %v", d.Consts)
	}
	var greet *ExportedFunc
	for i := range d.ExportedFuncs {
		if d.ExportedFuncs[i].Name == "greet" {
			greet = &d.ExportedFuncs[i]
		}
	}
	if greet == nil {
		t.Fatalf("greet not found: %+v", d.ExportedFuncs)
	}
	if !strings.Contains(greet.Signature, "def greet(name)") {
		t.Errorf("greet.Signature = %q", greet.Signature)
	}
	if greet.Doc != "Doc for greet" {
		t.Errorf("greet.Doc = %q", greet.Doc)
	}
	foundClass, foundMod := false, false
	for _, tp := range d.ExportedTypes {
		if tp.Name == "MyClass" {
			foundClass = true
			if len(tp.Fields) == 0 {
				t.Errorf("MyClass should have attr_accessor field, got %v", tp.Fields)
			}
		}
		if tp.Name == "MyModule" {
			foundMod = true
		}
	}
	if !foundClass || !foundMod {
		t.Errorf("types = %+v", d.ExportedTypes)
	}
}

func TestExtract_python_extended(t *testing.T) {
	src := []byte(`"""Module docstring.

Details here.
"""
import os
from django.models import Model

CONSTANT = 42
_private = 1

def public_func(x, y):
    """Public docstring."""
    pass

def _private_func():
    pass

class PublicClass:
    """Public class docstring."""
    field_a: int
    field_b: str = "hi"
`)
	d, err := Extract(src, "python")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(d.PackageDoc, "Module docstring") {
		t.Errorf("PackageDoc = %q", d.PackageDoc)
	}
	var pub *ExportedFunc
	for i := range d.ExportedFuncs {
		if d.ExportedFuncs[i].Name == "public_func" {
			pub = &d.ExportedFuncs[i]
		}
	}
	if pub == nil {
		t.Fatalf("public_func not found: %+v", d.ExportedFuncs)
	}
	if !strings.Contains(pub.Signature, "def public_func(x, y)") {
		t.Errorf("public_func signature = %q", pub.Signature)
	}
	if pub.Doc != "Public docstring." {
		t.Errorf("public_func doc = %q", pub.Doc)
	}
	var cls *ExportedType
	for i := range d.ExportedTypes {
		if d.ExportedTypes[i].Name == "PublicClass" {
			cls = &d.ExportedTypes[i]
		}
	}
	if cls == nil {
		t.Fatalf("PublicClass not found: %+v", d.ExportedTypes)
	}
	if cls.Doc != "Public class docstring." {
		t.Errorf("PublicClass doc = %q", cls.Doc)
	}
	if len(cls.Fields) != 2 {
		t.Errorf("PublicClass fields = %v", cls.Fields)
	}
	if !containsStr(d.Consts, "CONSTANT") {
		t.Errorf("missing CONSTANT: %v", d.Consts)
	}
	for _, fn := range d.ExportedFuncs {
		if fn.Name == "_private_func" {
			t.Errorf("_private_func should be filtered out")
		}
	}
	if !containsStr(d.Imports, "os") || !containsStr(d.Imports, "django.models") {
		t.Errorf("imports = %v", d.Imports)
	}
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func findFunc(d *StructuralData, name string) *ExportedFunc {
	for i := range d.ExportedFuncs {
		if d.ExportedFuncs[i].Name == name {
			return &d.ExportedFuncs[i]
		}
	}
	return nil
}

func findType(d *StructuralData, name string) *ExportedType {
	for i := range d.ExportedTypes {
		if d.ExportedTypes[i].Name == name {
			return &d.ExportedTypes[i]
		}
	}
	return nil
}

func mustExtract(t *testing.T, src, lang string) *StructuralData {
	t.Helper()
	d, err := Extract([]byte(src), lang)
	if err != nil {
		t.Fatalf("Extract(%s): %v", lang, err)
	}
	return d
}

func TestExtract_python_decoratedAndShebang(t *testing.T) {
	d := mustExtract(t, `#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""Module doc."""

@dataclass
class Point:
    """A point."""
    x: int

@functools.cache
def compute(n):
    """Computes."""
    return n

@decorator
def _hidden():
    pass

@dataclass
class _Private:
    pass
`, "python")
	if d.PackageDoc != "Module doc." {
		t.Errorf("PackageDoc = %q", d.PackageDoc)
	}
	if p := findType(d, "Point"); p == nil || p.Doc != "A point." || len(p.Fields) != 1 {
		t.Errorf("Point = %+v", p)
	}
	if f := findFunc(d, "compute"); f == nil || f.Signature != "def compute(n)" || f.Doc != "Computes." {
		t.Errorf("compute = %+v", f)
	}
	if findFunc(d, "_hidden") != nil || findType(d, "_Private") != nil {
		t.Errorf("private decorated symbols leaked: %v %v", d.ExportFns, d.ExportTypes)
	}
}

func TestExtract_rust_visibilityAttrsEnumsAliases(t *testing.T) {
	d := mustExtract(t, `/// Doc for Shape.
#[derive(Debug, Clone)]
#[non_exhaustive]
pub enum Shape {
    Circle(f64),
    Square { side: f64 },
}

/// Doc for Id.
pub type Id = u64;

/// Crate only.
pub(crate) fn crate_fn() {}
pub(super) struct SuperStruct;
pub(in crate::a) const SCOPED: u8 = 1;
enum PrivEnum { A }
type PrivAlias = u8;

/// Doc for run.
#[inline]
pub fn run() {}
`, "rust")
	if s := findType(d, "Shape"); s == nil || s.Doc != "Doc for Shape." || len(s.Fields) != 2 {
		t.Errorf("Shape = %+v", s)
	}
	if id := findType(d, "Id"); id == nil || id.Doc != "Doc for Id." {
		t.Errorf("Id = %+v", id)
	}
	if f := findFunc(d, "run"); f == nil || f.Doc != "Doc for run." {
		t.Errorf("run = %+v", f)
	}
	if findFunc(d, "crate_fn") != nil {
		t.Errorf("pub(crate) fn should not be exported")
	}
	for _, n := range []string{"SuperStruct", "PrivEnum", "PrivAlias"} {
		if findType(d, n) != nil {
			t.Errorf("%s should not be exported", n)
		}
	}
	if containsStr(d.Consts, "SCOPED") {
		t.Errorf("SCOPED should not be exported: %v", d.Consts)
	}
}

func TestExtract_ruby_magicCommentsAndMethods(t *testing.T) {
	d := mustExtract(t, `#!/usr/bin/env ruby
# frozen_string_literal: true
# -*- mode: ruby -*-

# Library doc.
require 'json'

# frozen_string_literal: true
class Foo
  # Doc for bar.
  def bar(x)
  end

  def self.build; end

  private

  def secret; end

  public

  def visible; end

  protected

  def guarded; end

  module Inner
    def helper; end
  end
end
`, "ruby")
	if d.PackageDoc != "Library doc." {
		t.Errorf("PackageDoc = %q", d.PackageDoc)
	}
	if foo := findType(d, "Foo"); foo == nil || foo.Doc != "" {
		t.Errorf("Foo = %+v (magic comment must not be its doc)", foo)
	}
	if f := findFunc(d, "Foo#bar"); f == nil || f.Signature != "def bar(x)" || f.Doc != "Doc for bar." {
		t.Errorf("Foo#bar = %+v", f)
	}
	if f := findFunc(d, "Foo.build"); f == nil || f.Signature != "def self.build" {
		t.Errorf("Foo.build = %+v", f)
	}
	if findFunc(d, "Foo#visible") == nil || findFunc(d, "Foo::Inner#helper") == nil || findType(d, "Foo::Inner") == nil {
		t.Errorf("funcs = %v types = %v", d.ExportFns, d.ExportTypes)
	}
	if findFunc(d, "Foo#secret") != nil || findFunc(d, "Foo#guarded") != nil {
		t.Errorf("private/protected methods leaked: %v", d.ExportFns)
	}
}

func TestExtract_go_groupsConstsFieldsInterfaces(t *testing.T) {
	d := mustExtract(t, `package p

// Group doc.
type (
	// A is a.
	A struct {
		X, y int
		Emb
		*pkg.Ptr
		lower
		z string
		Tagged int `+"`json:\"t\"`"+`
	}
	// B is b.
	B interface {
		Do(x int) error
		io.Reader
		hidden()
	}
	C int
	d int
)

// Single doc.
type (
	Single int
)

const X, y, Z = 1, 2, 3

const (
	Ä = 1
)
`, "golang")
	a := findType(d, "A")
	if a == nil || a.Doc != "A is a." {
		t.Fatalf("A = %+v", a)
	}
	wantA := []string{"X int", "Emb", "*pkg.Ptr", "Tagged int `json:\"t\"`"}
	if strings.Join(a.Fields, "|") != strings.Join(wantA, "|") {
		t.Errorf("A.Fields = %q, want %q", a.Fields, wantA)
	}
	if b := findType(d, "B"); b == nil || b.Doc != "B is b." || strings.Join(b.Fields, "|") != "Do(x int) error|io.Reader" {
		t.Errorf("B = %+v", b)
	}
	if c := findType(d, "C"); c == nil || c.Doc != "" {
		t.Errorf("C must not inherit the group doc: %+v", c)
	}
	if s := findType(d, "Single"); s == nil || s.Doc != "Single doc." {
		t.Errorf("Single = %+v", s)
	}
	if findType(d, "d") != nil {
		t.Errorf("unexported d leaked")
	}
	for _, want := range []string{"X", "Z", "Ä"} {
		if !containsStr(d.Consts, want) {
			t.Errorf("missing const %s: %v", want, d.Consts)
		}
	}
	if containsStr(d.Consts, "y") {
		t.Errorf("unexported y leaked: %v", d.Consts)
	}
}

func TestExtract_typescript_arrowsAliasesEnumsClauses(t *testing.T) {
	d := mustExtract(t, `/* eslint-disable */
// license
/**
 * Module doc.
 */
import { x } from 'x';
export { y } from './y';

/** Adds. */
export const add = (a: number, b: number): number => a + b;
export const mul = function (a, b) { return a * b; };
export type Opts = { a: number; b?: string };
export enum Color { Red, Green = 2 }
export const { destructured, other } = obj;
export const [first] = arr;
function local(a: string) {}
class Local {}
function notExported() {}
const inner = () => 1;
export { local, Local as Renamed };
`, "typescript")
	if d.PackageDoc != "Module doc." {
		t.Errorf("PackageDoc = %q", d.PackageDoc)
	}
	if !containsStr(d.Imports, "./y") {
		t.Errorf("re-export source missing from imports: %v", d.Imports)
	}
	if f := findFunc(d, "add"); f == nil || f.Signature != "const add = (a: number, b: number): number =>" || f.Doc != "Adds." {
		t.Errorf("add = %+v", f)
	}
	if f := findFunc(d, "mul"); f == nil || f.Signature != "const mul = function (a, b)" {
		t.Errorf("mul = %+v", f)
	}
	if o := findType(d, "Opts"); o == nil || len(o.Fields) != 2 {
		t.Errorf("Opts = %+v", o)
	}
	if c := findType(d, "Color"); c == nil || strings.Join(c.Fields, "|") != "Red|Green = 2" {
		t.Errorf("Color = %+v", c)
	}
	if findFunc(d, "local") == nil || findType(d, "Local") == nil {
		t.Errorf("export clause names missing: %v %v", d.ExportFns, d.ExportTypes)
	}
	if findFunc(d, "notExported") != nil || findFunc(d, "inner") != nil {
		t.Errorf("non-exported leaked: %v", d.ExportFns)
	}
	for _, n := range append(append([]string{}, d.ExportFns...), d.Consts...) {
		if strings.ContainsAny(n, "{[") {
			t.Errorf("destructuring pattern recorded as name: %q", n)
		}
	}
}

func TestExtract_js_packageDocBelongsToDecl(t *testing.T) {
	d := mustExtract(t, `/* Copyright ACME */

/** Doc for f. */
export function f() {}
`, "javascript")
	if d.PackageDoc != "" {
		t.Errorf("PackageDoc = %q, want empty", d.PackageDoc)
	}
	if f := findFunc(d, "f"); f == nil || f.Doc != "Doc for f." {
		t.Errorf("f = %+v", f)
	}
}

func TestExtract_tsx(t *testing.T) {
	d := mustExtract(t, `export const App = (p: Props) => <div>{p.x}</div>;
export function Button(): JSX.Element { return <button/>; }
`, LangForExt(".tsx"))
	if findFunc(d, "App") == nil || findFunc(d, "Button") == nil {
		t.Errorf("tsx funcs = %+v", d.ExportedFuncs)
	}
}

func TestExtract_java_licenseEnumsRecords(t *testing.T) {
	d := mustExtract(t, `/*
 * Licensed under Apache 2.0.
 */
package com.example;

/* not javadoc */
public class Foo {}

/** Colors. */
public enum Color { RED, GREEN; public int code() { return 1; } }

public record Pair(int a, String b) {}

interface Hidden { void x(); }
enum PrivEnum { A }
`, "java")
	if d.PackageDoc != "" {
		t.Errorf("PackageDoc = %q, license must not be used", d.PackageDoc)
	}
	if foo := findType(d, "Foo"); foo == nil || foo.Doc != "" {
		t.Errorf("Foo = %+v", foo)
	}
	if c := findType(d, "Color"); c == nil || c.Doc != "Colors." || strings.Join(c.Fields, "|") != "RED|GREEN" {
		t.Errorf("Color = %+v", c)
	}
	if findFunc(d, "code") == nil {
		t.Errorf("enum public method missing: %v", d.ExportFns)
	}
	if p := findType(d, "Pair"); p == nil || strings.Join(p.Fields, "|") != "int a|String b" {
		t.Errorf("Pair = %+v", p)
	}
	if findType(d, "Hidden") != nil || findType(d, "PrivEnum") != nil {
		t.Errorf("non-public types leaked: %v", d.ExportTypes)
	}

	d = mustExtract(t, `/** Package doc. */
package com.example;
`, "java")
	if d.PackageDoc != "Package doc." {
		t.Errorf("package-info PackageDoc = %q", d.PackageDoc)
	}
}

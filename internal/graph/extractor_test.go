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
		{".tsx", "typescript"},
		{".js", "javascript"},
		{".jsx", "javascript"},
		{".rs", "rust"},
		{".java", "java"},
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

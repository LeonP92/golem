package graph

import (
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

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

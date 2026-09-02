package main

import (
	"bytes"
	"testing"
)

func TestDispatchUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := dispatch([]string{"bogus"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if stderr.String() == "" {
		t.Fatal("expected an error message on stderr")
	}
}

func TestDispatchNoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := dispatch([]string{}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}

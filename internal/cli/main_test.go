package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Agent runs write a tool-policy hook that names the golem binary; the tests
// only need one to exist on PATH.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "golem-stub-")
	if err != nil {
		panic(err)
	}
	name := "golem"
	if runtime.GOOS == "windows" {
		name = "golem.exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		panic(err)
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

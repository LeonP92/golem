package prereq

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGitCapability(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git is a POSIX shell script")
	}
	for _, ok := range []bool{false, true} {
		dir := t.TempDir()
		body := "#!/bin/sh\necho 'git version 2.34.1'\n"
		if ok {
			body = "#!/bin/sh\necho 'usage: check-attr --source <tree>'\nexit 129\n"
		}
		os.WriteFile(filepath.Join(dir, "git"), []byte(body), 0755)
		t.Setenv("PATH", dir)
		if err := Git(); (err == nil) != ok {
			t.Fatalf("supported %v: %v", ok, err)
		}
	}
}

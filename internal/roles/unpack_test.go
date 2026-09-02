package roles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnpackWritesAllDefaultRoleFiles(t *testing.T) {
	dir := t.TempDir()
	written, err := Unpack(dir)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if len(written) != len(RoleNames) {
		t.Fatalf("wrote %d files, want %d", len(written), len(RoleNames))
	}
	for _, name := range RoleNames {
		path := filepath.Join(dir, "roles", name+".md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to exist: %v", path, err)
		}
	}
}

func TestUnpackNeverOverwritesExistingRoleFile(t *testing.T) {
	dir := t.TempDir()
	rolesDir := filepath.Join(dir, "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	customized := "# My Customized Developer Role\ncustom content"
	if err := os.WriteFile(filepath.Join(rolesDir, "developer.md"), []byte(customized), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Unpack(dir); err != nil {
		t.Fatalf("Unpack: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(rolesDir, "developer.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != customized {
		t.Fatal("Unpack overwrote a customized role file — it must be repo-owned after first init (spec: Distribution & Ownership)")
	}
}

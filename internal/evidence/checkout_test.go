package evidence

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretFreeCheckout(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env.example"), []byte("SYNTHETIC=true"), 0644)
	if err := CheckCheckout(dir); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ".env"), []byte("synthetic-only"), 0644)
	if err := CheckCheckout(dir); err == nil {
		t.Fatal("live env file accepted")
	}
}

func TestCheckCheckoutIgnoresDependenciesAndOtherWorktrees(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{".venv/lib/certifi/cacert.pem", ".golem/tickets/x/worktree/.env", "testdata/server.key", "vendor/x/id_rsa"} {
		path := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckCheckout(root); err != nil {
		t.Fatalf("dependency and fixture files refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckCheckout(root); err == nil {
		t.Fatal("a live .env at the root was accepted")
	}
}

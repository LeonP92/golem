package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEpicPlanValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	good := `{"requirements":[{"id":"R1","text":"x"}],"nodes":[{"key":"T1","title":"t","acceptance_commands":["go test ./..."],"requirement_ids":["R1"]}],"edges":[]}`
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := EpicPlanValidate([]string{"--target", "epic/abc", path}, &out, &errb); code != 0 {
		t.Fatalf("valid plan: code = %d, stderr = %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "1 requirement(s), 1 node(s)") {
		t.Errorf("summary missing: %s", out.String())
	}

	// A wrong branch target is every bit as invalid as a structural error.
	bad := strings.Replace(good, `"requirement_ids":["R1"]`, `"requirement_ids":["R1"],"branch_target":"main"`, 1)
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := EpicPlanValidate([]string{"--target", "epic/abc", path}, &out, &errb); code == 0 {
		t.Fatal("a node targeting the wrong ref must fail")
	}
	if !strings.Contains(out.String(), `targets "main"`) {
		t.Errorf("branch-target error must be listed: %s", out.String())
	}

	if code := EpicPlanValidate([]string{path}, &out, &errb); code == 0 {
		t.Fatal("--target is required")
	}
}

package gating

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/config"
)

// outsideOf returns a path next to root, never inside it.
func outsideOf(root string) string {
	return filepath.Join(filepath.Dir(root), "elsewhere", "file")
}

func TestNormalizedHookCalls(t *testing.T) {
	root := t.TempDir()
	p := config.PolicyConfig{AllowWorktreeOnly: true, AllowNetwork: []string{"allowed.test"}}
	for _, tc := range []struct {
		call  Call
		allow bool
	}{
		{Call{Tool: "Bash", Command: "git push origin feature"}, false},
		{Call{Tool: "bash", Command: "echo x; git push origin feature"}, false},
		{Call{Tool: "read", Path: ".env.local"}, false},
		{Call{Tool: "bash", Command: "cp ../../.env .env"}, false},
		{Call{Tool: "Edit", Path: outsideOf(root)}, false},
		{Call{Tool: "Edit", Path: "main.go"}, true},
		{Call{Tool: "Read", Path: ".env.example"}, true},
		{Call{Tool: "bash", Command: "curl https://blocked.test/"}, false},
		{Call{Tool: "bash", Command: "curl https://allowed.test/"}, true},
		{Call{Tool: "bash", Command: `curl "https://blocked.test/"`}, false},
	} {
		if d := Check(p, root, tc.call); d.Allowed != tc.allow {
			t.Fatalf("%+v: %+v", tc.call, d)
		}
	}
	if _, err := DecodeCall(strings.NewReader(`{}`)); err == nil {
		t.Fatal("missing event accepted")
	}
}

func TestPatchPathsAndWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	outside := outsideOf(root)
	p := config.PolicyConfig{AllowWorktreeOnly: true}
	for _, tc := range []struct {
		patch string
		allow bool
	}{
		{"*** Begin Patch\n*** Update File: main.go\n@@\n+safe\n*** End Patch", true},
		{"*** Begin Patch\n*** Add File: ../escape\n+x\n*** End Patch", false},
		{"*** Begin Patch\n*** Update File: main.go\n*** Move to: " + outside + "\n*** End Patch", false},
		{"*** Begin Patch\n*** Add File: .env\n+x\n*** End Patch", false},
		{"unsupported", false},
	} {
		for _, tool := range []string{"apply_patch", "patch"} {
			if d := Check(p, root, Call{Tool: tool, Patch: tc.patch, CWD: root}); d.Allowed != tc.allow {
				t.Fatalf("%s %q: %+v", tool, tc.patch, d)
			}
		}
	}
	if d := Check(p, filepath.Join(root, "worktree"), Call{Tool: "Edit", Path: "other.go", CWD: root}); d.Allowed {
		t.Fatal("relative path resolved against wrong cwd")
	}
}

func TestUnknownToolsAndMultipleHosts(t *testing.T) {
	root := t.TempDir()
	p := config.PolicyConfig{AllowWorktreeOnly: true, AllowNetwork: []string{"allowed.test"}}
	if d := Check(p, root, Call{Tool: "custom_mutator"}); d.Allowed {
		t.Fatal("unknown tool bypasses policy")
	}
	if d := Check(p, root, Call{Tool: "WebSearch"}); d.Allowed {
		t.Fatal("WebSearch must stay denied")
	}
	if d := Check(p, root, Call{Tool: "bash", Command: "curl https://allowed.test/ https://blocked.test/"}); d.Allowed {
		t.Fatal("second network target bypasses policy")
	}
}

func TestToolNamesAcrossBackends(t *testing.T) {
	root := t.TempDir()
	outside := outsideOf(root)
	p := config.PolicyConfig{AllowWorktreeOnly: true, AllowNetwork: []string{"allowed.test"}}
	for _, tc := range []struct {
		name  string
		call  Call
		allow bool
	}{
		{"MultiEdit inside", Call{Tool: "MultiEdit", Path: "a.go"}, true},
		{"MultiEdit outside", Call{Tool: "MultiEdit", Path: outside}, false},
		{"multiedit outside", Call{Tool: "multiedit", Path: outside}, false},
		{"MultiEdit env", Call{Tool: "MultiEdit", Path: ".env"}, false},
		{"NotebookEdit inside", Call{Tool: "NotebookEdit", Path: "n.ipynb"}, true},
		{"NotebookEdit outside", Call{Tool: "NotebookEdit", Path: outside}, false},
		{"BashOutput", Call{Tool: "BashOutput"}, true},
		{"KillShell", Call{Tool: "KillShell"}, true},
		{"Task", Call{Tool: "Task"}, true},
		{"Agent", Call{Tool: "Agent"}, true},
		{"Skill", Call{Tool: "Skill"}, true},
		{"ToolSearch", Call{Tool: "ToolSearch"}, true},
		{"TaskCreate", Call{Tool: "TaskCreate"}, true},
		{"TaskUpdate", Call{Tool: "TaskUpdate"}, true},
		{"TaskList", Call{Tool: "TaskList"}, true},
		{"TaskGet", Call{Tool: "TaskGet"}, true},
		{"AskUserQuestion", Call{Tool: "AskUserQuestion"}, true},
		{"ExitPlanMode", Call{Tool: "ExitPlanMode"}, true},
		{"EnterPlanMode", Call{Tool: "EnterPlanMode"}, true},
		{"KillBash", Call{Tool: "KillBash"}, true},
		{"TodoWrite", Call{Tool: "TodoWrite"}, true},
		{"WebFetch allowed", Call{Tool: "WebFetch", URL: "https://allowed.test/x"}, true},
		{"WebFetch blocked", Call{Tool: "WebFetch", URL: "https://blocked.test/x"}, false},
		{"webfetch blocked", Call{Tool: "webfetch", URL: "http://blocked.test:8080/"}, false},
		{"WebFetch no url", Call{Tool: "WebFetch"}, false},
		{"WebFetch bad url", Call{Tool: "WebFetch", URL: "::"}, false},
		{"WebSearch", Call{Tool: "WebSearch"}, false},
	} {
		if d := Check(p, root, tc.call); d.Allowed != tc.allow {
			t.Fatalf("%s: %+v", tc.name, d)
		}
	}
}

func TestDecodeNotebookPathAndURL(t *testing.T) {
	call, err := DecodeCall(strings.NewReader(`{"tool_name":"NotebookEdit","tool_input":{"notebook_path":"/x/n.ipynb"}}`))
	if err != nil || call.Path != "/x/n.ipynb" {
		t.Fatalf("%+v %v", call, err)
	}
	call, err = DecodeCall(strings.NewReader(`{"tool_name":"WebFetch","tool_input":{"url":"https://a.test/"}}`))
	if err != nil || call.URL != "https://a.test/" {
		t.Fatalf("%+v %v", call, err)
	}
}

func TestCommandStartMatching(t *testing.T) {
	root := t.TempDir()
	p := config.PolicyConfig{AllowWorktreeOnly: true}
	for _, tc := range []struct {
		command string
		allow   bool
	}{
		{`git commit -m "drop sudo usage"`, true},
		{`git commit -m "eval harness"`, true},
		{`git commit -m "never git push here; ok"`, true},
		{`grep -rn source .`, true},
		{`source .venv/bin/activate && pytest`, true},
		{`eval "$(ssh-agent)"`, true},
		{`echo git push`, true},
		{`python -m twine upload dist/*`, false},
		{`twine upload dist/*`, false},
		{`git push`, false},
		{`git push origin main`, false},
		{`git push --force`, false},
		{`ls && git push`, false},
		{`ls || git push`, false},
		{`ls | sudo tee x`, false},
		{`(git push)`, false},
		{"echo `git push`", false},
		{`echo $(git push)`, false},
		{`bash -c "git push"`, false},
		{`sudo rm x`, false},
		{"ls\ngit push", false},
	} {
		if d := Check(p, root, Call{Tool: "bash", Command: tc.command}); d.Allowed != tc.allow {
			t.Fatalf("%q allow=%v: %+v", tc.command, tc.allow, d)
		}
	}
}

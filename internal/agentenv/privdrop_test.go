package agentenv

import (
	"os/exec"
	"testing"
)

// overrideEnv is what stops the dropped process inheriting root's identity.
// HOME matters most: it is on the allow-list, so it arrives as /root, which
// the unprivileged agent cannot write — and Claude Code keeps its state under
// HOME, so getting this wrong means every phase fails on a directory it
// cannot create.
func TestOverrideEnv(t *testing.T) {
	in := []string{"PATH=/usr/bin", "HOME=/root", "USER=root", "LANG=C"}
	out := overrideEnv(in, map[string]string{
		"HOME": "/home/golem-agent", "USER": "golem-agent", "LOGNAME": "golem-agent",
	})

	got := map[string]string{}
	for _, kv := range out {
		k, v, _ := cutEnv(kv)
		if _, dup := got[k]; dup {
			t.Errorf("%s appears twice; the last one wins in exec and it may not be ours", k)
		}
		got[k] = v
	}
	for k, want := range map[string]string{
		"HOME": "/home/golem-agent", "USER": "golem-agent",
		"LOGNAME": "golem-agent",           // absent from the input: must be appended
		"PATH":    "/usr/bin", "LANG": "C", // untouched
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

// Outside a container — every test run, CLI mode, `go run` — there is no
// account to drop to and no privilege to drop. DropPrivileges must leave the
// command alone rather than setting a Credential that would fail with EPERM.
func TestDropPrivileges_NoAccountIsANoOp(t *testing.T) {
	t.Setenv(UserEnv, "")
	// Reset the once-guard so this test sees the empty setting.
	agentOnce = onceReset()
	agentAcct = nil

	cmd := exec.Command("true")
	cmd.Env = []string{"HOME=/root"}
	DropPrivileges(cmd)

	if hasCredential(cmd) {
		t.Error("DropPrivileges set a Credential with no agent account configured; " +
			"the exec would fail with EPERM wherever this is not root")
	}
	if len(cmd.Env) != 1 || cmd.Env[0] != "HOME=/root" {
		t.Errorf("environment was rewritten with no account configured: %v", cmd.Env)
	}
}

// EnsureOwnership must not walk or chown anything when there is no account.
func TestEnsureOwnership_NoAccountIsANoOp(t *testing.T) {
	t.Setenv(UserEnv, "")
	agentOnce = onceReset()
	agentAcct = nil

	if err := EnsureOwnership(t.TempDir()); err != nil {
		t.Errorf("EnsureOwnership with no account = %v, want nil", err)
	}
	if err := EnsureOwnership(""); err != nil {
		t.Errorf("EnsureOwnership(\"\") = %v, want nil", err)
	}
}

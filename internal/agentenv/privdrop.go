package agentenv

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// The shem itself stays root: it holds the push credential, and dropping it
// would mean the credential lives somewhere the agent can reach anyway.
// UserEnv names the unprivileged account the agent runs as.
const UserEnv = "GOLEM_AGENT_USER"

// agentAccount is the resolved unprivileged user, or nil when privileges
// cannot or should not be dropped.
type agentAccount struct {
	UID, GID uint32
	Name     string
	Home     string
}

var (
	agentOnce sync.Once
	agentAcct *agentAccount
)

// onceReset exists so tests can re-resolve after changing UserEnv.
func onceReset() sync.Once { return sync.Once{} }

// resolveAgentUser returns the account to run agents as, or nil to run them
// as this process does.
//
// nil is returned when the process is not root (nothing to drop — this is
// CLI mode, `go run`, and every test) or when the account does not exist.
// The container case is the one that matters, and it is verified there:
// running the agent as root is what makes GOLEM_GITHUB_TOKEN readable out of
// /proc/1/environ, so a silent fallback to root would quietly restore exactly
// the exposure this exists to close. It is therefore logged loudly.
// User returns the resolved unprivileged agent account, or nil.
func User() *agentAccount {
	agentOnce.Do(func() {
		name := os.Getenv(UserEnv)
		if name == "" {
			return
		}
		if os.Geteuid() != 0 {
			// Not root: there is no privilege to drop, and setting
			// Credential would fail with EPERM.
			return
		}
		u, err := user.Lookup(name)
		if err != nil {
			log.Printf("agentenv: SECURITY: %s=%q but that account does not exist; "+
				"the agent will run as root and can read the push credential from "+
				"/proc/1/environ: %v", UserEnv, name, err)
			return
		}
		uid, err1 := strconv.ParseUint(u.Uid, 10, 32)
		gid, err2 := strconv.ParseUint(u.Gid, 10, 32)
		if err1 != nil || err2 != nil {
			log.Printf("agentenv: SECURITY: %s=%q has a non-numeric uid/gid (%q/%q); "+
				"the agent will run as root", UserEnv, name, u.Uid, u.Gid)
			return
		}
		home := u.HomeDir
		if home == "" {
			home = filepath.Join("/home", name)
		}
		agentAcct = &agentAccount{UID: uint32(uid), GID: uint32(gid), Name: name, Home: home}
	})
	return agentAcct
}

// applyAgentCredentials makes cmd run as the unprivileged agent account, and
// rewrites the identity variables so the tools it starts agree about who it
// is. HOME especially: it is on agentenv's allow-list and would otherwise
// arrive as /root, which the dropped process cannot write, and Claude Code
// stores its state under HOME.
// DropPrivileges makes cmd run as the unprivileged agent account when one is
// configured, and is a no-op otherwise.
func DropPrivileges(cmd *exec.Cmd) {
	acct := User()
	if acct == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: acct.UID, Gid: acct.GID}
	cmd.Env = overrideEnv(cmd.Env, map[string]string{
		"HOME":    acct.Home,
		"USER":    acct.Name,
		"LOGNAME": acct.Name,
	})
}

// overrideEnv replaces or appends each name=value in env.
func overrideEnv(env []string, over map[string]string) []string {
	out := make([]string, 0, len(env)+len(over))
	seen := make(map[string]bool, len(over))
	for _, kv := range env {
		name, _, _ := cutEnv(kv)
		if v, ok := over[name]; ok {
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name+"="+v)
			continue
		}
		out = append(out, kv)
	}
	for name, v := range over {
		if !seen[name] {
			out = append(out, name+"="+v)
		}
	}
	return out
}

func cutEnv(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return kv, "", false
}

// ensureAgentOwnership gives the agent account ownership of everything under
// root that it does not already own.
//
// The shem runs as root and creates the clone, the ticket directory and the
// worktree, so without this the dropped agent cannot write the code it is
// employed to write. Entries already owned are skipped, so the common case —
// a repository prepared on an earlier run — walks the tree without touching
// it.
//
// Failures are reported but not fatal: a repository the agent cannot write
// fails loudly in the phase that follows, which is a better error than
// refusing to start.
// EnsureOwnership gives the agent account ownership of everything under root
// that it does not already own. No-op when no account is configured.
func EnsureOwnership(root string) error {
	acct := User()
	if acct == nil || root == "" {
		return nil
	}
	var fixed, failed int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is not a reason to abandon the rest.
			return nil //nolint:nilerr
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if ok && st.Uid == acct.UID && st.Gid == acct.GID {
			return nil
		}
		// Lchown, not Chown: following a symlink here would hand ownership
		// of whatever it points at — including something outside the
		// repository — to the agent account.
		if chErr := os.Lchown(path, int(acct.UID), int(acct.GID)); chErr != nil {
			failed++
			return nil
		}
		fixed++
		return nil
	})
	if fixed > 0 || failed > 0 {
		log.Printf("agentenv: agent ownership under %s: %d fixed, %d failed", root, fixed, failed)
	}
	if err != nil {
		return fmt.Errorf("preparing %s for the agent account: %w", root, err)
	}
	return nil
}

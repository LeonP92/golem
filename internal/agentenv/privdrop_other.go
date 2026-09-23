//go:build !unix

package agentenv

import (
	"io/fs"
	"os/exec"
)

// Dropping to another account is a container (Linux) concern. On other
// platforms User() never resolves an account — os.Geteuid is -1 — so these
// are never reached with a real account; they exist so the package builds.

func setCredential(*exec.Cmd, uint32, uint32) {}

func hasCredential(*exec.Cmd) bool { return false }

func ownedBy(fs.FileInfo, uint32, uint32) bool { return false }

//go:build unix

package agentenv

import (
	"io/fs"
	"os/exec"
	"syscall"
)

// setCredential makes cmd run as uid/gid.
func setCredential(cmd *exec.Cmd, uid, gid uint32) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uid, Gid: gid}
}

// hasCredential reports whether cmd has been set to run as another user.
func hasCredential(cmd *exec.Cmd) bool {
	return cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil
}

// ownedBy reports whether info is already owned by uid:gid.
func ownedBy(info fs.FileInfo, uid, gid uint32) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uid && st.Gid == gid
}

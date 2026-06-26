//go:build windows

package control

import (
	"os/exec"
	"syscall"
)

// detachSysProc fully detaches the spawned sidecar on Windows: DETACHED_PROCESS
// (no inherited console) + CREATE_NEW_PROCESS_GROUP, so it keeps running after
// this short-lived control-mcp process exits.
func detachSysProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200}
}

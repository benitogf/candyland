//go:build !windows

package control

import (
	"os/exec"
	"syscall"
)

// detachSysProc fully detaches the spawned sidecar from this short-lived
// control-mcp process: Setsid puts it in its own session so it keeps running
// after the MCP tool call returns and the control-mcp exits.
func detachSysProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

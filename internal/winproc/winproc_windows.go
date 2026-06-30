//go:build windows

package winproc

import (
	"os/exec"
	"syscall"
)

// createNoWindow (CREATE_NO_WINDOW): run the spawned process without allocating a
// console, so a headless candyland run doesn't flash a command window for every
// claude/git/gh process it launches. HideWindow covers the case where a console
// is inherited.
const createNoWindow = 0x08000000

// Configure keeps spawned processes windowless on Windows. It preserves any
// SysProcAttr the caller already set, layering the windowless flags on top, so a
// caller that also needs other attributes (e.g. the agent spawn's job-object
// setup) doesn't lose them.
func Configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

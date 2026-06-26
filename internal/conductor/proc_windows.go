//go:build windows

package conductor

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW: run the spawned process without allocating a console, so a
// headless candyland run doesn't flash a command window for every claude/comms
// process it launches. HideWindow covers the case where a console is inherited.
const createNoWindow = 0x08000000

// configureProc keeps spawned processes windowless on Windows (no POSIX process
// groups here; kill stays best-effort on the child itself).
func configureProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

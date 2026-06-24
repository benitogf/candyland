//go:build !windows

package conductor

import (
	"os/exec"
	"syscall"
)

// configureProc puts the child in its own process group so the whole tree can be
// killed together — the real `claude` CLI spawns child processes, and a bare
// Process.Kill() would orphan them (and leave the stdout pipe open, hanging us).
func configureProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree kills the child and its process group, best-effort.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative pid targets the process group led by the child (Setpgid above).
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

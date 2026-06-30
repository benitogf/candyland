//go:build !windows

package conductor

import (
	"os/exec"
	"syscall"

	"github.com/benitogf/candyland/internal/winproc"
)

// configureProc puts the child in its own process group so the whole tree can be
// killed together — the real `claude` CLI spawns child processes, and a bare
// Process.Kill() would orphan them (and leave the stdout pipe open, hanging us).
// It also routes through the shared windowless helper (a no-op on Unix) so every
// spawn path shares one config.
func configureProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	winproc.Configure(cmd)
}

// afterStart is a post-Start hook the Windows path uses to assign the process to
// its job object. Nothing to do on Unix — the process group set above is enough.
func afterStart(cmd *exec.Cmd) {}

// killTree kills the child and its process group, best-effort.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative pid targets the process group led by the child (Setpgid above).
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

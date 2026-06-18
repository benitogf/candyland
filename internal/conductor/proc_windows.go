//go:build windows

package conductor

import "os/exec"

// configureProc is a no-op on Windows (no POSIX process groups). Kill is
// best-effort on the child process itself.
func configureProc(cmd *exec.Cmd) {}

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

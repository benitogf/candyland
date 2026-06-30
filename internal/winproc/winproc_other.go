//go:build !windows

// Package winproc centralizes OS-specific process spawn configuration so every
// exec site in candyland routes through one helper. On non-Windows hosts the
// windowless settings are meaningless, so Configure is a no-op.
package winproc

import "os/exec"

// Configure makes a spawned process windowless on Windows; a no-op elsewhere.
// Callers MUST NOT overwrite cmd.SysProcAttr after calling this — they should
// set their own fields (e.g. POSIX process-group flags) before or have the
// platform file own SysProcAttr entirely.
func Configure(cmd *exec.Cmd) {}

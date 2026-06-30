//go:build windows

package conductor

import (
	"os/exec"
	"sync"
	"unsafe"

	"github.com/benitogf/candyland/internal/winproc"
	"golang.org/x/sys/windows"
)

// On Windows there are no POSIX process groups, so killing the direct child
// (Process.Kill) orphans the grandchildren the real `claude` CLI spawns. To kill
// the WHOLE tree we put the agent process in a Job Object created with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE: closing the job's last handle terminates
// every process still assigned to it. Children a process creates after it joins
// the job are assigned to the same job by default, so the whole tree goes down
// when killTree closes the handle. This is the Windows analogue of proc_unix.go's
// process-group kill — the Unix path is unchanged.

// jobs maps a spawned *exec.Cmd to its job-object handle so killTree can find and
// close it. Entries are removed on close to avoid leaking handles across runs.
var jobs sync.Map // *exec.Cmd -> windows.Handle

// configureProc keeps spawned processes windowless (shared helper) and, for the
// agent spawn, prepares the job-object machinery. The job is created here (before
// Start) so afterStart can assign the freshly started process to it.
func configureProc(cmd *exec.Cmd) {
	winproc.Configure(cmd)

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return // best-effort: fall back to single-process kill in killTree
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	jobs.Store(cmd, job)
}

// afterStart assigns the running process to its job object. The process and any
// children it spawns afterward belong to the job, so closing the job in killTree
// kills the entire tree.
func afterStart(cmd *exec.Cmd) {
	v, ok := jobs.Load(cmd)
	if !ok || cmd.Process == nil {
		return
	}
	job := v.(windows.Handle)
	h, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(cmd.Process.Pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.AssignProcessToJobObject(job, h)
}

// killTree closes the job object (which terminates the whole tree via
// KILL_ON_JOB_CLOSE) and kills the direct child as a backstop when no job was
// created. Best-effort, idempotent.
func killTree(cmd *exec.Cmd) {
	if v, ok := jobs.LoadAndDelete(cmd); ok {
		_ = windows.CloseHandle(v.(windows.Handle)) // kill-on-close terminates the tree
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

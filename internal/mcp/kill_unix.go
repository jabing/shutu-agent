//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// prepareProcessGroup starts the server in its own process group so the whole
// group (the server and any child it spawns) can be signalled together
// (mirrors internal/tools/kill_unix.go).
func prepareProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree signals the whole process group with SIGKILL and then the direct
// child, stopping the server and any grandchild that would otherwise keep the
// pipes open.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// A negative pid targets the process group (Setpgid above).
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

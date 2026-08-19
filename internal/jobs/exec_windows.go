//go:build windows

package jobs

import "os/exec"

// prepareJobProcessGroup is a no-op on Windows: there are no POSIX process
// groups and taskkill /T is unavailable under the harness sandbox (access
// denied), so tree termination reduces to killing the direct child we own
// (mirrors internal/tools/kill_windows.go).
func prepareJobProcessGroup(cmd *exec.Cmd) {}

// killJobTree terminates the direct child process. The command's output goes
// to a temp file (not a pipe), so Wait returns as soon as this direct child
// exits even if a grandchild (e.g. ping spawned by cmd.exe) lingers without
// holding our pipe. A lingering grandchild is a documented residual risk
// (docs/decisions/2026-08-18-m3-sandbox-scope.md).
func killJobTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

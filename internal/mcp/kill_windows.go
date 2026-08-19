//go:build windows

package mcp

import "os/exec"

// prepareProcessGroup is a no-op on Windows: there are no POSIX process groups
// and taskkill /T is unavailable under the harness sandbox (access denied), so
// tree termination reduces to killing the direct child we own (mirrors
// internal/tools/kill_windows.go).
func prepareProcessGroup(cmd *exec.Cmd) {}

// killTree terminates the direct MCP server process. The server's stdout is an
// os.Pipe we own, so after the direct child dies the pipe closes and any
// synchronous reader unblocks; a lingering grandchild that outlives the direct
// child is a documented residual risk (same posture as M3).
func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

//go:build !windows

package terminal

import (
	"os/exec"
)

// shellCommand 返回配置好 shell 的 *exec.Cmd（尚未 Start）：/bin/sh，
// 若 opts.Shell 为空则用 "/bin/sh"，参数 = append(opts.Args, "-i")（交互）。
func shellCommand(opts SessionOpts) *exec.Cmd {
	shell := opts.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	args := append(append([]string{}, opts.Args...), "-i")
	return exec.Command(shell, args...)
}

// killProcessTree 终止 cmd：简化实现，直接 Process.Kill()。
func killProcessTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// interruptInput 尽力而为的中断：向 s.stdin 写入字节 0x03（Ctrl+C）。
func interruptInput(s *Session) {
	if s != nil && s.stdin != nil {
		_, _ = s.stdin.Write([]byte{0x03})
	}
}

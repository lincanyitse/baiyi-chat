//go:build windows

package mcp

import (
	"context"
	"os/exec"
	"syscall"
)

// buildCommand 在 Windows 上构造 MCP server 子进程。
//
// 关键：npx / node 这类命令在 Windows 上是 .cmd 批处理，
// exec.Command 无法直接执行，必须用 cmd /c 包装，
// 否则会报 "executable file not found"。
func buildCommand(ctx context.Context, cfg ServerConfig) *exec.Cmd {
	if cfg.Command == "" {
		return nil
	}

	var cmd *exec.Cmd
	if needsShellWrap(cfg.Command) {
		args := append([]string{"/c", cfg.Command}, cfg.Args...)
		cmd = exec.CommandContext(ctx, "cmd", args...)
	} else {
		cmd = exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	}

	// 隐藏控制台窗口，避免每次调用弹出黑框
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// needsShellWrap 判断命令是否是需要 cmd /c 包装的批处理。
func needsShellWrap(name string) bool {
	for _, ext := range []string{".cmd", ".bat"} {
		if hasSuffixFold(name, ext) {
			return true
		}
	}
	return false
}

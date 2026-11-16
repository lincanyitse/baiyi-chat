//go:build !windows

package mcp

import (
	"context"
	"os/exec"
)

// buildCommand 在类 Unix 系统上直接执行命令。
func buildCommand(ctx context.Context, cfg ServerConfig) *exec.Cmd {
	if cfg.Command == "" {
		return nil
	}
	return exec.CommandContext(ctx, cfg.Command, cfg.Args...)
}

func needsShellWrap(string) bool { return false }

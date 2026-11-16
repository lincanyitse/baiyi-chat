package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/mcp"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// fakeConn 是假的 MCP 连接，不启动任何外部进程。
type fakeConn struct {
	tools  []*mcp.Tool
	closed bool
	broken bool
}

func (f *fakeConn) Healthy() bool { return !f.broken && !f.closed }
func (f *fakeConn) LastError() string {
	if f.broken {
		return "假故障"
	}
	return ""
}
func (f *fakeConn) Tools() []*mcp.Tool { return f.tools }
func (f *fakeConn) Close() error {
	f.closed = true
	return nil
}

// fakeTool 是最小可注册工具。
type fakeTool struct{ name string }

func (f fakeTool) Spec() tool.Spec {
	return tool.DefaultSpec(tool.Spec{
		Name:        f.name,
		Description: "测试工具",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Risk:        tool.RiskRead,
	})
}

func (f fakeTool) Execute(context.Context, json.RawMessage, *tool.ExecContext) (tool.Result, error) {
	return tool.TextResult("ok"), nil
}

// newTestServiceForMCP 构造一个只关心工具集的最小 Service。
func newTestServiceForMCP(t *testing.T, reg *tool.Registry) *session.Service {
	t.Helper()
	return session.NewService(session.Options{
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tools: reg.Enabled(),
	})
}

// dialFail 返回一个总是失败的 dial。
func dialFail(msg string) dialFunc {
	return func(context.Context, config.MCPServer, *slog.Logger) (mcpConn, error) {
		return nil, errors.New(msg)
	}
}

// errTestDial 是测试用的固定连接错误。
var errTestDial = errors.New("测试：连不上")

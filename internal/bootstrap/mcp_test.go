package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// newTestManager 构造一个用假 dial 的管理器。
func newTestManager(t *testing.T, dial dialFunc) (*MCPManager, *tool.Registry, *session.Service) {
	t.Helper()
	reg := tool.NewRegistry()
	svc := newTestServiceForMCP(t, reg)
	m := NewMCPManager(reg, svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if dial != nil {
		m.dial = dial
	}
	return m, reg, svc
}

// TestApplyDisabledServerIsNoop 禁用的 server 不该被连接。
func TestApplyDisabledServerIsNoop(t *testing.T) {
	m, _, _ := newTestManager(t, dialFail("不该被调用"))

	results := m.Apply(context.Background(), []config.MCPServer{
		{Alias: "off", Transport: "http", URL: "http://127.0.0.1:9/mcp", Enabled: false},
	})
	if err := results["off"]; err != nil {
		t.Fatalf("禁用的 server 不该报错: %v", err)
	}
	if n := len(m.clients); n != 0 {
		t.Fatalf("禁用的 server 不该被连接，实际连接 %d 个", n)
	}
}

// TestApplyFailingServerDoesNotBlockOthers 单个失败不拖垮整批。
func TestApplyFailingServerDoesNotBlockOthers(t *testing.T) {
	m, _, _ := newTestManager(t, nil)
	m.dial = func(_ context.Context, sc config.MCPServer, _ *slog.Logger) (mcpConn, error) {
		if sc.Alias == "bad" {
			return nil, errTestDial
		}
		return &fakeConn{}, nil
	}

	results := m.Apply(context.Background(), []config.MCPServer{
		{Alias: "bad", Transport: "stdio", Command: "x", Enabled: true},
		{Alias: "good", Transport: "http", URL: "http://x", Enabled: true},
	})
	if results["bad"] == nil {
		t.Error("连不上的 server 应返回错误")
	}
	if results["good"] != nil {
		t.Errorf("其他 server 不该被影响: %v", results["good"])
	}
	if _, ok := m.clients["good"]; !ok {
		t.Error("good 应该已连接")
	}
}

// TestApplyRemovesDeletedServer 配置里删掉的 server 要断开。
func TestApplyRemovesDeletedServer(t *testing.T) {
	m, _, _ := newTestManager(t, nil)

	fake := &fakeConn{}
	m.clients["gone"] = fake
	m.cfgs["gone"] = config.MCPServer{Alias: "gone", Enabled: true}

	m.Apply(context.Background(), nil)

	if _, ok := m.clients["gone"]; ok {
		t.Fatal("删除的 server 应被断开")
	}
	if !fake.closed {
		t.Fatal("删除的 server 的连接应被关闭")
	}
}

// TestApplyIsIdempotent 同样的配置连调两次，不该重建连接。
func TestApplyIsIdempotent(t *testing.T) {
	m, _, _ := newTestManager(t, dialFail("不该重连"))

	fake := &fakeConn{}
	m.clients["p"] = fake
	m.cfgs["p"] = config.MCPServer{Alias: "p", Transport: "http", URL: "http://x", Enabled: true}

	m.Apply(context.Background(), []config.MCPServer{
		{Alias: "p", Transport: "http", URL: "http://x", Enabled: true},
	})

	if m.clients["p"] != fake {
		t.Fatal("配置未变时不该重建连接")
	}
	if fake.closed {
		t.Fatal("配置未变时不该关闭连接")
	}
}

// TestApplyReconnectsOnConfigChange 地址变了必须重连。
func TestApplyReconnectsOnConfigChange(t *testing.T) {
	m, _, _ := newTestManager(t, dialFail("故意的"))

	fake := &fakeConn{}
	m.clients["p"] = fake
	m.cfgs["p"] = config.MCPServer{Alias: "p", Transport: "http", URL: "http://old", Enabled: true}

	m.Apply(context.Background(), []config.MCPServer{
		{Alias: "p", Transport: "http", URL: "http://new", Enabled: true},
	})

	if !fake.closed {
		t.Fatal("配置变化时应关闭旧连接")
	}
}

// TestApplyReconnectsUnhealthy 连接还在但不健康时应当重连。
func TestApplyReconnectsUnhealthy(t *testing.T) {
	dialed := 0
	m, _, _ := newTestManager(t, func(context.Context, config.MCPServer, *slog.Logger) (mcpConn, error) {
		dialed++
		return &fakeConn{}, nil
	})

	broken := &fakeConn{broken: true}
	m.clients["p"] = broken
	m.cfgs["p"] = config.MCPServer{Alias: "p", Transport: "http", URL: "http://x", Enabled: true}

	m.Apply(context.Background(), []config.MCPServer{
		{Alias: "p", Transport: "http", URL: "http://x", Enabled: true},
	})

	if dialed != 1 {
		t.Fatalf("不健康的连接应触发重连，实际 dial %d 次", dialed)
	}
	if !broken.closed {
		t.Fatal("旧连接应被关闭")
	}
}

// TestApplyRefreshesServiceTools 工具集变了要刷新 Service 快照。
func TestApplyRefreshesServiceTools(t *testing.T) {
	m, reg, svc := newTestManager(t, nil)

	if len(svc.CurrentTools()) != 0 {
		t.Fatal("前置条件：初始不该有工具")
	}

	if err := reg.Register(fakeTool{name: "newtool"}); err != nil {
		t.Fatal(err)
	}
	m.Apply(context.Background(), nil)

	found := false
	for _, tl := range svc.CurrentTools() {
		if tl.Spec().Name == "newtool" {
			found = true
		}
	}
	if !found {
		t.Fatal("Apply 后 Service 应看到新注册的工具——它复制的是切片，不刷新等于没变")
	}
}

// TestReconnectUnknownAlias 未知 alias 要报错，不能静默成功。
func TestReconnectUnknownAlias(t *testing.T) {
	m, _, _ := newTestManager(t, nil)
	if err := m.Reconnect(context.Background(), "nope"); err == nil {
		t.Fatal("未知 alias 应报错")
	}
}

// TestSameConfig 配置比对只关心连接相关字段。
func TestSameConfig(t *testing.T) {
	base := config.MCPServer{
		Alias: "a", Transport: "stdio", Command: "npx", Args: []string{"-y", "x"},
	}
	same := base
	if !sameConfig(base, same) {
		t.Fatal("完全相同的配置应视为相同")
	}

	diffArgs := base
	diffArgs.Args = []string{"-y", "y"}
	if sameConfig(base, diffArgs) {
		t.Fatal("参数不同应触发重连")
	}

	// Risk 变化不需要重连：它在每次 Spec() 时读
	riskOnly := base
	riskOnly.Risk = "dangerous"
	if !sameConfig(base, riskOnly) {
		t.Fatal("只改 Risk 不该触发重连")
	}
}

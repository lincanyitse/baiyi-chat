package bootstrap

// MCP 在运行中的热管理。
//
// 启动时连接一次的做法对普通用户不可接受：加一个 server 要重启进程、
// 丢进行中的轮次。这里把它改成可反复调用的 Apply。
//
// 关键约束：
//   1. Apply 必须幂等——界面无脑全量提交，只有差异才动。
//   2. 单个 server 失败不能拖垮其他的，也不能让整次保存失败。
//   3. 删掉或换掉 server 后，它的工具必须从注册表里消失，
//      否则模型还能调到一个已经不存在的连接。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/mcp"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// mcpConn 是 Manager 对一个外部 server 连接的依赖面。
//
// 抽成接口是为了可测：真实实现是 *mcp.Client，测试可以塞假的，
// 不必真的起一个外部进程。
type mcpConn interface {
	Healthy() bool
	LastError() string
	Tools() []*mcp.Tool
	Close() error
}

// dialFunc 建连接。默认用 mcp.NewClient().Connect，测试可替换。
type dialFunc func(ctx context.Context, cfg config.MCPServer, log *slog.Logger) (mcpConn, error)

// MCPManager 持有运行中的 MCP 连接，并让它们与配置保持一致。
type MCPManager struct {
	reg  *tool.Registry
	svc  *session.Service
	log  *slog.Logger
	dial dialFunc

	mu sync.Mutex
	// clients 是当前已连接的 server，键是 alias。
	clients map[string]mcpConn
	// cfgs 是每个 client 的配置快照，用于判断「配置变了没有」。
	cfgs map[string]config.MCPServer
}

// NewMCPManager 构造管理器。
func NewMCPManager(reg *tool.Registry, svc *session.Service, log *slog.Logger) *MCPManager {
	return &MCPManager{
		reg:     reg,
		svc:     svc,
		log:     log,
		dial:    realDial,
		clients: map[string]mcpConn{},
		cfgs:    map[string]config.MCPServer{},
	}
}

// realDial 连一个真实的 MCP server。
func realDial(ctx context.Context, sc config.MCPServer, log *slog.Logger) (mcpConn, error) {
	client := mcp.NewClient(mcp.ServerConfig{
		Alias:        sc.Alias,
		Transport:    sc.Transport,
		Command:      sc.Command,
		Args:         sc.Args,
		URL:          sc.URL,
		AllowedTools: sc.AllowedTools,
		Risk:         sc.Risk,
	}, log)
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

// Apply 让运行中的连接与目标配置一致，返回每个 alias 的结果。
//
// 只处理有差异的 server：
//   - 新增 → 连接
//   - 删除 / 禁用 → 断开并移除工具
//   - 配置变更 → 断开重连
//   - 未变且健康 → 不动
//
// 返回的 map 包含每个启用 server 的结果；连接失败的那个只有它自己
// 有错误，其余照常工作。
func (m *MCPManager) Apply(ctx context.Context, servers []config.MCPServer) map[string]error {
	m.mu.Lock()
	defer m.mu.Unlock()

	results := make(map[string]error, len(servers))

	wanted := make(map[string]config.MCPServer, len(servers))
	for _, s := range servers {
		if s.Alias == "" {
			continue
		}
		wanted[s.Alias] = s
	}

	// 先移除不再需要或配置变了的
	for alias, client := range m.clients {
		target, keep := wanted[alias]
		if keep && target.Enabled && sameConfig(m.cfgs[alias], target) {
			continue
		}
		m.removeLocked(alias, client)
	}

	// 再补齐需要连的
	for alias, sc := range wanted {
		if !sc.Enabled {
			results[alias] = nil // 禁用不是错误
			continue
		}
		if _, ok := m.clients[alias]; ok {
			// 仍存在且配置未变 → 检查健康度，不健康的顺手重连
			if m.clients[alias].Healthy() {
				results[alias] = nil
				continue
			}
			m.removeLocked(alias, m.clients[alias])
		}
		results[alias] = m.connectLocked(ctx, sc)
	}

	// 工具集变了，必须刷新 Service 的快照——它复制的是切片，
	// 不刷新等于改了注册表但模型仍拿到旧的。
	m.svc.SetTools(m.reg.Enabled())
	return results
}

// Status 返回每个 server 的连接状态。
func (m *MCPManager) Status() map[string]ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string]ServerStatus, len(m.cfgs))
	for alias, cfg := range m.cfgs {
		st := ServerStatus{
			Alias:     alias,
			Transport: cfg.Transport,
			Enabled:   cfg.Enabled,
			Risk:      cfg.Risk,
			Tools:     []string{},
		}
		if c, ok := m.clients[alias]; ok {
			st.Connected = c.Healthy()
			st.LastError = c.LastError()
			for _, t := range c.Tools() {
				st.Tools = append(st.Tools, t.Spec().Name)
			}
		}
		out[alias] = st
	}
	return out
}

// Reconnect 单体重连。
func (m *MCPManager) Reconnect(ctx context.Context, alias string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sc, ok := m.cfgs[alias]
	if !ok {
		return fmt.Errorf("未配置 MCP server %q", alias)
	}

	if c, ok := m.clients[alias]; ok {
		m.removeLocked(alias, c)
	}
	if err := m.connectLocked(ctx, sc); err != nil {
		return err
	}
	m.svc.SetTools(m.reg.Enabled())
	return nil
}

// Close 断开全部连接。
func (m *MCPManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for alias, c := range m.clients {
		m.removeLocked(alias, c)
	}
	return nil
}

// connectTimeout 限制单次连接与工具发现的耗时。
//
// MCP server 是任意外部进程：它可能卡在启动、可能握手后不再响应。
// 没有超时的话，启动会永远挂住，保存界面也会一直转圈——
// 用户既看不出问题在哪，也没法取消。
const connectTimeout = 20 * time.Second

// connectLocked 建连接并注册工具。调用方必须持有 m.mu。
func (m *MCPManager) connectLocked(ctx context.Context, sc config.MCPServer) error {
	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	client, err := m.dial(dialCtx, sc, m.log)
	if err != nil {
		// 失败也要记进 cfgs：下次 Apply 才能识别「配置没变但上次失败了」，
		// 从而重试而不是当成新 server。
		m.cfgs[sc.Alias] = sc
		msg := err.Error()
		if errors.Is(dialCtx.Err(), context.DeadlineExceeded) {
			msg = fmt.Sprintf("连接超时（%s）：server 没有响应", connectTimeout)
		}
		m.log.Error("MCP server 连接失败", "alias", sc.Alias, "err", msg)
		return fmt.Errorf("连接 %s 失败: %s", sc.Alias, msg)
	}

	registered := 0
	for _, t := range client.Tools() {
		if err := m.reg.Register(t); err != nil {
			m.log.Warn("注册 MCP 工具失败",
				"alias", sc.Alias, "tool", t.Spec().Name, "err", err)
			continue
		}
		registered++
	}

	m.clients[sc.Alias] = client
	m.cfgs[sc.Alias] = sc
	m.log.Info("MCP server 就绪", "alias", sc.Alias, "tools", registered)
	return nil
}

// removeLocked 断开一个 server 并移除它的工具。调用方必须持有 m.mu。
func (m *MCPManager) removeLocked(alias string, c mcpConn) {
	for _, t := range c.Tools() {
		m.reg.Unregister(t.Spec().Name)
	}
	_ = c.Close()
	delete(m.clients, alias)
	delete(m.cfgs, alias)
	m.log.Info("MCP server 已移除", "alias", alias)
}

// sameConfig 判断两份配置是否需要重连。
//
// 只比连接相关字段：Enabled 与 Risk 的变化不需要重连
// （Risk 在每次 Spec() 时读，Enabled 由 Apply 的增删逻辑处理）。
func sameConfig(a, b config.MCPServer) bool {
	if a.Transport != b.Transport || a.Command != b.Command || a.URL != b.URL {
		return false
	}
	return sameStrings(a.Args, b.Args) && sameStrings(a.AllowedTools, b.AllowedTools)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ServerStatus 是对外暴露的连接状态。
type ServerStatus struct {
	Alias     string
	Transport string
	Enabled   bool
	Risk      string
	Connected bool
	LastError string
	Tools     []string
}

// Package mcp 封装 MCP 客户端：连接 server、发现工具、适配成 tool.Tool。
//
// 依赖约束：本包只依赖 tool 与 llm，不依赖 agent——
// MCP 工具就是普通的 Tool 实现，运行时无需感知它来自哪里。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// ServerConfig 描述一个 MCP server。
type ServerConfig struct {
	// Alias 是短名，用于工具命名空间，必须唯一且简短。
	Alias string
	// Transport 是 "stdio" 或 "http"。
	Transport string
	// Command / Args 用于 stdio。
	Command string
	Args    []string
	// URL 用于 http。
	URL string
	// AllowedTools 为空表示全部允许；非空时只暴露列出的工具。
	AllowedTools []string
	// Risk 是这个 server 全部工具的风险级别：read / write / dangerous。
	//
	// 不按工具名猜（「write_file」一定是写？）：工具名由外部 server 定义，
	// 不可信。让用户显式声明，界面说明后果。
	Risk string
}

// Client 管理一个 MCP server 的连接与工具。
type Client struct {
	cfg     ServerConfig
	log     *slog.Logger
	mu      sync.RWMutex
	session *mcp.ClientSession
	tools   []*Tool
	healthy bool
	lastErr string
}

// NewClient 构造客户端（不立即连接）。
func NewClient(cfg ServerConfig, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{cfg: cfg, log: logger, healthy: true}
}

// Alias 返回 server 短名。
func (c *Client) Alias() string { return c.cfg.Alias }

// Connect 建立连接并发现工具。
func (c *Client) Connect(ctx context.Context) error {
	var transport mcp.Transport
	switch c.cfg.Transport {
	case "stdio":
		cmd := buildCommand(ctx, c.cfg)
		if cmd == nil {
			return fmt.Errorf("server %q: stdio 传输需要 command", c.cfg.Alias)
		}
		transport = &mcp.CommandTransport{Command: cmd}
	case "http", "streamable-http", "":
		if c.cfg.URL == "" {
			return fmt.Errorf("server %q: http 传输需要 url", c.cfg.Alias)
		}
		transport = &mcp.StreamableClientTransport{Endpoint: c.cfg.URL}
	default:
		return fmt.Errorf("server %q: 不支持的传输 %q", c.cfg.Alias, c.cfg.Transport)
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name: "baiyi", Version: "0.1.0",
	}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		c.mu.Lock()
		c.healthy = false
		c.lastErr = err.Error()
		c.mu.Unlock()
		return fmt.Errorf("连接 server %q: %w", c.cfg.Alias, err)
	}

	c.mu.Lock()
	c.session = session
	c.healthy = true
	c.lastErr = ""
	c.mu.Unlock()

	return c.discoverTools(ctx)
}

// discoverTools 拉取工具列表并适配成 tool.Tool。
func (c *Client) discoverTools(ctx context.Context) error {
	c.mu.RLock()
	session := c.session
	c.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("server %q 未连接", c.cfg.Alias)
	}

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("列出 server %q 的工具: %w", c.cfg.Alias, err)
	}

	allowed := make(map[string]bool, len(c.cfg.AllowedTools))
	for _, n := range c.cfg.AllowedTools {
		allowed[n] = true
	}

	var adapted []*Tool
	for _, t := range res.Tools {
		if len(allowed) > 0 && !allowed[t.Name] {
			continue // 白名单外的工具不暴露
		}
		adapted = append(adapted, &Tool{
			client:  c,
			raw:     t,
			exposed: ExposedName(c.cfg.Alias, t.Name),
		})
	}

	c.mu.Lock()
	c.tools = adapted
	c.mu.Unlock()

	c.log.Info("MCP server 已连接",
		"alias", c.cfg.Alias, "transport", c.cfg.Transport, "tools", len(adapted))
	return nil
}

// Tools 返回已发现的工具。
func (c *Client) Tools() []*Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Tool, len(c.tools))
	copy(out, c.tools)
	return out
}

// Healthy 报告连接是否可用。
func (c *Client) Healthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.healthy && c.session != nil
}

// LastError 返回最近一次失败的原因。健康时为空串。
func (c *Client) LastError() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastErr
}

// Reconnect 断开后重新连接并重新发现工具。
//
// 配置不可变——要换配置就换个 Client。这里只处理「连接断了」，
// 让用户不必重启整个进程。
func (c *Client) Reconnect(ctx context.Context) error {
	_ = c.Close()

	c.mu.Lock()
	c.healthy = true
	c.lastErr = ""
	c.mu.Unlock()

	return c.Connect(ctx)
}

// Close 断开连接。
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		err := c.session.Close()
		c.session = nil
		return err
	}
	return nil
}

// markUnhealthy 在某次调用失败后标记，避免整轮依赖故障 server。
func (c *Client) markUnhealthy(err error) {
	c.mu.Lock()
	c.healthy = false
	c.lastErr = err.Error()
	c.mu.Unlock()
}

// Tool 是适配后的 MCP 工具。
type Tool struct {
	client  *Client
	raw     *mcp.Tool
	exposed string
}

var _ tool.Tool = (*Tool)(nil)

// Spec 实现 tool.Tool。
//
// MCP 工具描述视为**不可信输入**（可能是提示词注入载体），
// 因此在描述前加上来源标注，让模型知道这是外部提供的内容。
func (t *Tool) Spec() tool.Spec {
	schema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	if t.raw.InputSchema != nil {
		if b, err := json.Marshal(t.raw.InputSchema); err == nil {
			schema = b
		}
	}

	desc := t.raw.Description
	if desc == "" {
		desc = "（无描述）"
	}
	desc = fmt.Sprintf("[来自 MCP server %q 的外部工具] %s", t.client.cfg.Alias, desc)

	return tool.DefaultSpec(tool.Spec{
		Name:        t.exposed,
		Description: desc,
		InputSchema: schema,
		// 默认按只读处理：白名单能限制暴露范围，但表达不了
		// 「这个写操作需要审批」。要审批的在配置里显式标 write/dangerous。
		Risk:    t.client.risk(),
		Timeout: 60 * 1e9, // 60s
	})
}

// risk 把配置里的字符串映射成工具风险级别。
//
// 未知值一律按 dangerous 处理：让用户填错时受到更严格的约束，
// 而不是悄悄降级成「不需要审批」。
func (c *Client) risk() tool.RiskLevel {
	switch c.cfg.Risk {
	case "write":
		return tool.RiskWrite
	case "dangerous":
		return tool.RiskDangerous
	case "", "read":
		return tool.RiskRead
	default:
		return tool.RiskDangerous
	}
}

// Execute 调用远端工具。
func (t *Tool) Execute(ctx context.Context, in json.RawMessage, ec *tool.ExecContext) (tool.Result, error) {
	t.client.mu.RLock()
	session := t.client.session
	t.client.mu.RUnlock()

	if session == nil {
		return tool.ErrorResult("MCP server 未连接"), nil
	}

	var args map[string]any
	if len(in) > 0 {
		if err := json.Unmarshal(in, &args); err != nil {
			return tool.ErrorResult("参数解析失败: " + err.Error()), nil
		}
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      t.raw.Name,
		Arguments: args,
	})
	if err != nil {
		t.client.markUnhealthy(err)
		return tool.ErrorResult("调用 MCP 工具失败: " + err.Error()), nil
	}

	// MCP 返回的内容视为外部数据，标记污点
	if ec != nil && ec.Taint != nil {
		ec.Taint.MarkExternal("mcp:" + t.client.cfg.Alias)
	}

	text := extractText(res)
	if res.IsError {
		return tool.ErrorResult(text), nil
	}
	return tool.TextResult(text), nil
}

// extractText 把 MCP 返回的内容块拼成文本。
func extractText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			b.WriteString(v.Text)
		default:
			// 图片等非文本内容暂以占位符表示
			b.WriteString("[非文本内容]")
		}
	}
	if b.Len() == 0 {
		return "（工具无输出）"
	}
	return b.String()
}

// ExposedName 生成暴露给模型的工具名：mcp__<alias>__<tool>。
//
// 若超过 64 字符或含非法字符，退化为带哈希的短名——
// 保持确定性，重启后仍能反查。
func ExposedName(alias, toolName string) string {
	full := "mcp__" + alias + "__" + sanitizePart(toolName)
	if len(full) <= 64 && isClean(full) {
		return full
	}
	return ShortName(alias, toolName)
}

func sanitizePart(s string) string {
	var b strings.Builder
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func isClean(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

var _ = llm.BlockText

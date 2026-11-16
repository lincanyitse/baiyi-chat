package llm

import (
	"context"
	"encoding/json"
)

// Request 是归一化后的推理请求。
type Request struct {
	Model       string    `json:"model"`
	System      string    `json:"system,omitempty"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
	Stream      bool      `json:"stream"`
	Thinking    *Thinking `json:"thinking,omitempty"`
	// CacheBreakpoints 是需要显式标记缓存断点的消息索引（仅 Anthropic 使用）。
	CacheBreakpoints []int `json:"cache_breakpoints,omitempty"`
	// Tools 是本次可用的工具定义。空表示不提供工具。
	Tools []ToolSpec `json:"tools,omitempty"`
	// ToolChoice 控制工具调用策略：auto（默认）/ none / required。
	ToolChoice string `json:"tool_choice,omitempty"`
}

// ToolSpec 是面向模型的工具定义。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// 工具调用策略。
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
)

// Thinking 描述推理预算。
type Thinking struct {
	Enabled      bool `json:"enabled"`
	BudgetTokens int  `json:"budget_tokens,omitempty"`
}

// Provider 是模型服务适配器的最小接口。
//
// 具体实现（openaicompat / anthropic）只负责把统一模型翻译成厂商协议，
// 并把厂商流翻译回归一化事件。上层永远不感知厂商差异。
type Provider interface {
	// Name 返回 Provider 名称，用于日志与账本。
	Name() string
	// Capabilities 返回指定模型的能力声明。
	Capabilities(model string) Capabilities
	// Stream 发起流式推理，返回归一化事件通道。
	// 错误通过 ErrorEvent 送达；返回 error 仅表示请求根本无法发起。
	Stream(ctx context.Context, req Request) (<-chan Event, error)
	// CountTokens 估算或精确计算请求 token 数。
	// 不支持精确计算时返回估算值。
	CountTokens(ctx context.Context, req Request) (int, error)
}

// ModelLister 是可选的扩展接口：能列出服务端可用模型的 Provider 实现它。
// 用于发现网关上一共有哪些模型，避免手工维护模型清单。
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ModelInfo 是服务端返回的模型条目。
type ModelInfo struct {
	ID      string
	OwnedBy string
	Created int64
}

// Package embed 提供外部推理能力：文本嵌入与重排。
//
// 依赖约束：只依赖 llm 与标准库。记账通过 Meter 回调注入，
// 避免 embed → cost 的耦合。
//
// 为什么独立成包而不放 rag：记忆的情节向量化也要用嵌入，
// 放 rag 会强制 memory → rag，把解析、切分、FTS 全拖进记忆的依赖闭包。
package embed

import (
	"context"
	"errors"
)

// Usage 是一次调用的用量，供成本记账。
type Usage struct {
	InputTokens int
	Requests    int
	Model       string
}

// Embedder 把文本转成向量。
type Embedder interface {
	// Embed 批量嵌入。返回的向量顺序必须与 texts 一致。
	Embed(ctx context.Context, model string, texts []string) ([][]float32, Usage, error)
	// Dimensions 返回该模型输出的维度。未知时返回 0。
	Dimensions(model string) int
	// Name 返回后端名称，用于日志与账本。
	Name() string
}

// Reranker 对候选文档按与查询的相关性重新排序。
type Reranker interface {
	// Rerank 返回与 docs 等长的相关性分数，越大越相关。
	Rerank(ctx context.Context, model, query string, docs []string) ([]float32, Usage, error)
	Name() string
}

// Meter 接收用量上报。接口开在本包（消费方），由 bootstrap 注入实现。
type Meter interface {
	// RecordEmbed 记录一次嵌入调用的用量。
	RecordEmbed(ctx context.Context, backend, model string, usage Usage) error
	// RecordRerank 记录一次重排调用的用量。
	RecordRerank(ctx context.Context, backend, model string, usage Usage) error
}

// 常见错误。
var (
	// ErrNotConfigured 表示该能力未配置。调用方应据此降级而非报错。
	ErrNotConfigured = errors.New("embed: 服务未配置")
	// ErrUnavailable 表示后端暂时不可用。调用方应据此降级为关键词检索。
	ErrUnavailable = errors.New("embed: 服务不可用")
	// ErrUnreachable 表示连不上服务（DNS 失败、拒绝连接、路由不可达）。
	//
	// 与 ErrUnavailable 分开是因为重试策略不同：服务端 5xx 可能下一秒
	// 就恢复，值得退避重试；而「连接被拒」通常是服务没启动或地址配错，
	// 重试只会让用户白等。两者都应该让调用方降级，但重试次数差很多。
	ErrUnreachable = errors.New("embed: 服务不可达")
	// ErrUnsupportedInput 表示输入本身被后端拒绝（超长、含非法字符等）。
	//
	// 重试无意义：同样的输入永远得到同样的拒绝。调用方应跳过这一条
	// 继续处理其余，而不是让整批失败。
	ErrUnsupportedInput = errors.New("embed: 输入不被接受")
)

// NoopMeter 是不记账的 Meter，用于测试与未配置记账的场景。
type NoopMeter struct{}

func (NoopMeter) RecordEmbed(context.Context, string, string, Usage) error  { return nil }
func (NoopMeter) RecordRerank(context.Context, string, string, Usage) error { return nil }

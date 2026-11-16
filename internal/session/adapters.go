package session

import (
	"context"
	"log/slog"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/agent"
	"github.com/lincanyitse/baiyi-chat/internal/contextx"
	"github.com/lincanyitse/baiyi-chat/internal/cost"
	"github.com/lincanyitse/baiyi-chat/internal/infra/idgen"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// storeAdapter 把 session 的持久化能力暴露给 agent.Store。
//
// 这样 agent 不 import session，而 session 可以 import agent——
// 依赖方向保持单一（L4 → L3）。工具结果走 Repository，SQL 留在 store。
type storeAdapter struct {
	svc  *Service
	repo Repository
}

var _ agent.Store = (*storeAdapter)(nil)

// AppendMessage 追加消息到 transcript。
func (a *storeAdapter) AppendMessage(ctx context.Context, sessionID string, role llm.Role, blocks []llm.Block) error {
	_, err := a.svc.appendMessage(ctx, sessionID, role, blocks, "")
	return err
}

// ToolResult 查询幂等缓存。
func (a *storeAdapter) ToolResult(ctx context.Context, turnID, callID string) (agent.StoredToolResult, bool, error) {
	return a.repo.GetToolResult(ctx, turnID, callID)
}

// SaveToolResult 写入幂等缓存。
func (a *storeAdapter) SaveToolResult(ctx context.Context, r agent.StoredToolResult) error {
	return a.repo.SaveToolResult(ctx, r)
}

// RecordUsage 上报一次模型调用的用量。
func (a *storeAdapter) RecordUsage(
	ctx context.Context,
	turnID, provider, model string, step int,
	usage llm.UsageEvent, latency, ttft time.Duration, errKind llm.ErrorKind,
) error {
	return a.svc.ledger.Record(ctx, cost.Record{
		TurnID: turnID, Step: step, Provider: provider, Model: model,
		Purpose: "chat", InputTokens: usage.Input, OutputTokens: usage.Output,
		CachedTokens: usage.Cached, ReasoningTokens: usage.Reasoning,
		Latency: latency, TTFT: ttft, ErrorKind: errKind,
	})
}

// snapshotSink 实现 agent.SnapshotSink，把上下文快照落进 context_snapshots。
//
// 走 Repository 而不是直接写 SQL：context_snapshots 的列名与 JSON
// 列格式属于 store 层的知识，散落在 session 里加列时要改两处。
type snapshotSink struct {
	repo Repository
	log  *slog.Logger
}

var _ agent.SnapshotSink = (*snapshotSink)(nil)

func (s *snapshotSink) SaveSnapshot(
	ctx context.Context, turnID string, step int, model string, out *contextx.Output,
) error {
	return s.repo.SaveSnapshot(ctx, ContextSnapshotInsert{
		ID:               idgen.New(),
		TurnID:           turnID,
		Step:             step,
		Model:            model,
		Segments:         out.Segments,
		Budget:           out.Budget,
		Total:            out.TotalTokens,
		Available:        out.Budget.Available,
		PrefixHash:       out.PrefixHash,
		CacheBreakpoints: out.CacheBreakpoints,
	})
}

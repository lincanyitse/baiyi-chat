package bootstrap

// 数据出口：会话导出与备份的装配。

import (
	"context"
	"fmt"

	"github.com/lincanyitse/baiyi-chat/internal/cost"
	"github.com/lincanyitse/baiyi-chat/internal/export"
)

// ExportSession 读取一个会话及其全部消息。
//
// 返回文件名用的标题：优先用会话标题，空则用 ID 前缀。
func (a *App) ExportSession(ctx context.Context, id string) (export.Doc, string, error) {
	sess, err := a.Repo.GetSession(ctx, id)
	if err != nil {
		return export.Doc{}, "", fmt.Errorf("会话不存在: %w", err)
	}

	msgs, err := a.Repo.ListMessages(ctx, id)
	if err != nil {
		return export.Doc{}, "", fmt.Errorf("读取消息失败: %w", err)
	}

	title := sess.Title
	if title == "" {
		title = "会话-" + id[:min(8, len(id))]
	}
	return export.Doc{Session: sess, Messages: msgs}, title, nil
}

// ListSessionsForExport 返回全部未删除会话的 ID。
func (a *App) ListSessionsForExport(ctx context.Context) ([]string, error) {
	sessions, err := a.Repo.ListSessions(ctx, 0) // 0 = 不限制
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.ID)
	}
	return out, nil
}

// SessionCost 汇总单个会话的全部轮次用量与费用。
//
// 用该会话的所有 turn 作为统计范围：一轮可能包含多次模型调用
// （工具调用之间的每一步都算），只数最后一条会严重低估。
func (a *App) SessionCost(ctx context.Context, sessionID string) (cost.Summary, error) {
	turns, err := a.Repo.ListTurns(ctx, sessionID)
	if err != nil {
		return cost.Summary{}, err
	}
	ids := make([]string, 0, len(turns))
	for _, t := range turns {
		ids = append(ids, t.ID)
	}
	return a.Ledger.SessionSummary(ctx, ids)
}

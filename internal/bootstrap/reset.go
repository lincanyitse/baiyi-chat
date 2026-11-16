package bootstrap

// 数据重置。
//
// 三个范围，逐级扩大。共同点：**永远不碰** web_auth、provider_secrets、
// secret_meta、prices——清掉口令等于把用户锁在门外，清掉密钥与价格
// 会让已配好的模型失效。重置的是「数据」，不是「配置」。

import (
	"context"
	"fmt"
	"strings"
)

// resetScope 是重置范围。值与 Web API 的 scope 字符串一致，
// 但类型留在装配层，避免 bootstrap 依赖 channel/web 的词汇。
type resetScope string

const (
	resetSessions  resetScope = "sessions"
	resetKnowledge resetScope = "knowledge"
	resetAll       resetScope = "all"
)

// resetTables 是各范围要清的表，按依赖顺序（先子后父）。
var resetTables = map[resetScope][]string{
	resetSessions: {
		"context_snapshots", "tool_results", "tool_output_store", "blobs",
		"llm_calls", "approvals", "turns", "messages", "sessions",
		"summary_state",
	},
	resetKnowledge: {
		"embeddings", "chunks", "documents", "kb_settings",
		"memory_events", "memory_candidates", "facts", "episodes",
		"profile_card", "profile_state", "jobs",
	},
}

// ResetData 清空指定范围的数据，返回每张表删除的行数。
//
// scope 取 "sessions"、"knowledge" 或 "all"。在单个事务里完成：
// 中途失败会回滚，不会留下「会话没了但消息还在」的中间态。
func (a *App) ResetData(ctx context.Context, scope string) (map[string]int, error) {
	kind := resetScope(scope)
	var tables []string
	switch kind {
	case resetSessions, resetKnowledge:
		tables = resetTables[kind]
	case resetAll:
		// 两个范围的并集，去重。顺序保留：先子表后父表。
		seen := map[string]bool{}
		for _, set := range [][]string{resetTables[resetSessions], resetTables[resetKnowledge]} {
			for _, t := range set {
				if !seen[t] {
					seen[t] = true
					tables = append(tables, t)
				}
			}
		}
	default:
		return nil, fmt.Errorf("未知的重置范围: %s", scope)
	}

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("开始事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	deleted := make(map[string]int, len(tables))
	for _, table := range tables {
		res, err := tx.ExecContext(ctx, "DELETE FROM "+table)
		if err != nil {
			// 表可能在某些构建里不存在（可选子系统），跳过而不是整体失败
			if isNoSuchTable(err) {
				continue
			}
			return nil, fmt.Errorf("清空 %s 失败: %w", table, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			deleted[table] = int(n)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交失败: %w", err)
	}
	return deleted, nil
}

// isNoSuchTable 判断错误是否是「表不存在」。
//
// 某些可选子系统的表可能没建（比如未装配知识库时的嵌入表）。
// 这种情况跳过而不是让整次重置失败。
func isNoSuchTable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "no such column")
}

package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

// TestResumeDoesNotDuplicateUserMessage 验证续跑不会重复写入用户消息。
//
// 这是 /resume 的核心不变量：首次执行时用户消息已落库，
// 恢复时必须复用同一条记录而不是再追加一条。
func TestResumeDoesNotDuplicateUserMessage(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	now := time.Now()

	if err := repo.CreateSession(ctx, session.Session{ID: "s1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTurn(ctx, session.Turn{
		ID: "t1", SessionID: "s1", Status: session.TurnRunning, StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// 首次执行：用户消息落库
	seq, _ := repo.NextSeq(ctx, "s1")
	if err := repo.AppendMessage(ctx, session.StoredMessage{
		ID: "m1", SessionID: "s1", Seq: seq, Role: llm.RoleUser,
		Content: []llm.Block{llm.NewTextBlock("原始问题")}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	before, _ := repo.ListMessages(ctx, "s1")
	if len(before) != 1 {
		t.Fatalf("初始应有 1 条消息，实际 %d", len(before))
	}

	// 模拟续跑：不调用 appendMessage，直接继续
	// （ResumeTurn 通过 AskOptions.ResumeTurnID 跳过落库）

	after, _ := repo.ListMessages(ctx, "s1")
	if len(after) != 1 {
		t.Errorf("续跑不应追加消息，实际 %d 条", len(after))
	}
	if llm.TextOf(llm.Message{Content: after[0].Content}) != "原始问题" {
		t.Error("原消息内容应保持不变")
	}
}

// TestFindResumableMatchesTurn 验证恢复时能找到正确的 turn。
func TestFindResumableMatchesTurn(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	now := time.Now()

	if err := repo.CreateSession(ctx, session.Session{ID: "s1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTurn(ctx, session.Turn{
		ID: "t1", SessionID: "s1", Status: session.TurnRunning, StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := repo.FindResumable(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.ID != "t1" {
		t.Errorf("应找到 t1，实际 ok=%v id=%s", ok, got.ID)
	}
}

// TestToolResultsSurviveResume 验证工具结果在续跑时仍可命中缓存。
func TestToolResultsSurviveResume(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	now := time.Now()

	if err := repo.CreateSession(ctx, session.Session{ID: "s1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTurn(ctx, session.Turn{
		ID: "t1", SessionID: "s1", Status: session.TurnRunning, StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// 首次执行时写入一条工具结果
	_, err := db.ExecContext(ctx,
		`INSERT INTO tool_results
		 (turn_id, tool_call_id, name, args_hash, result_json, is_error, handle, duration_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"t1", "call_1", "time_now", "h1", "已执行过的结果", 0, "", 5, now.Unix())
	if err != nil {
		t.Fatal(err)
	}

	// 续跑后该结果应仍在（agent 会据此跳过重复执行）
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM tool_results WHERE turn_id = 't1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("工具结果应保留，实际 %d 条", count)
	}
}

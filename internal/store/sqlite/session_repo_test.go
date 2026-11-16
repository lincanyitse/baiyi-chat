package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	infra "github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

func newRepo(t *testing.T) (*Repo, *infra.DB) {
	t.Helper()
	db, err := infra.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return New(db), db
}

func TestSessionCRUD(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	now := time.Now()

	s := session.Session{ID: "s1", Title: "测试", CreatedAt: now, UpdatedAt: now}
	if err := repo.CreateSession(ctx, s); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "测试" {
		t.Errorf("title = %q", got.Title)
	}

	if err := repo.UpdateSessionTitle(ctx, "s1", "新标题"); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetSession(ctx, "s1")
	if got.Title != "新标题" {
		t.Errorf("更新后 title = %q", got.Title)
	}

	list, err := repo.ListSessions(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Errorf("会话数 = %d, 期望 1", len(list))
	}

	if _, err := repo.GetSession(ctx, "nonexistent"); err == nil {
		t.Error("不存在的会话应返回错误")
	}
}

func TestMessageAppendAndList(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	now := time.Now()
	if err := repo.CreateSession(ctx, session.Session{ID: "s1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 3; i++ {
		seq, err := repo.NextSeq(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if seq != i {
			t.Errorf("第 %d 条消息 seq = %d", i, seq)
		}
		err = repo.AppendMessage(ctx, session.StoredMessage{
			ID: "m" + string(rune('0'+i)), SessionID: "s1", Seq: seq,
			Role: llm.RoleUser, Content: []llm.Block{llm.NewTextBlock("消息")},
			Trust: llm.TrustUser, CreatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	msgs, err := repo.ListMessages(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("消息数 = %d, 期望 3", len(msgs))
	}
	for i, m := range msgs {
		if m.Seq != i+1 {
			t.Errorf("消息 %d seq = %d", i, m.Seq)
		}
		if len(m.Content) != 1 {
			t.Errorf("消息 %d 内容块数 = %d", i, len(m.Content))
		}
	}
}

func TestEmptySessionNextSeqIsOne(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	now := time.Now()
	if err := repo.CreateSession(ctx, session.Session{ID: "s1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	seq, err := repo.NextSeq(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Errorf("空会话首条 seq = %d, 期望 1", seq)
	}
}

func TestTurnLifecycle(t *testing.T) {
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

	end := time.Now()
	if err := repo.UpdateTurnStatus(ctx, "t1", session.TurnDone, "", &end); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetTurn(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != session.TurnDone {
		t.Errorf("status = %q", got.Status)
	}
	if got.EndedAt == nil {
		t.Error("结束时间应已写入")
	}
}

func TestForeignKeyRejectsOrphanMessage(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	err := repo.AppendMessage(ctx, session.StoredMessage{
		ID: "m1", SessionID: "no-such-session", Seq: 1,
		Role: llm.RoleUser, Content: []llm.Block{llm.NewTextBlock("x")},
		CreatedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("应拒绝引用不存在会话的消息")
	}
}

func TestFindResumable(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	now := time.Now()

	if err := repo.CreateSession(ctx, session.Session{ID: "s1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// 已完成的轮次
	if err := repo.CreateTurn(ctx, session.Turn{
		ID: "done", SessionID: "s1", Status: session.TurnDone, StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// 卡在 running 的轮次（模拟进程被杀）
	if err := repo.CreateTurn(ctx, session.Turn{
		ID: "stuck", SessionID: "s1", Status: session.TurnRunning, StartedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := repo.FindResumable(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.ID != "stuck" {
		t.Errorf("应找到 stuck 轮次，实际 ok=%v got=%+v", ok, got)
	}
}

func TestFindResumableNone(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	now := time.Now()

	if err := repo.CreateSession(ctx, session.Session{ID: "s1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTurn(ctx, session.Turn{
		ID: "done", SessionID: "s1", Status: session.TurnDone, StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	_, ok, err := repo.FindResumable(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("全部完成时不应报告可恢复轮次")
	}
}

func TestFindResumableAwaiting(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	now := time.Now()

	if err := repo.CreateSession(ctx, session.Session{ID: "s1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTurn(ctx, session.Turn{
		ID: "await", SessionID: "s1", Status: session.TurnAwaiting, StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	got, ok, _ := repo.FindResumable(ctx, "s1")
	if !ok || got.ID != "await" {
		t.Errorf("awaiting 状态也应可恢复，实际 ok=%v", ok)
	}
}

func TestResumableSurvivesDBReopen(t *testing.T) {
	// 模拟进程被杀后重启：关库再用同一文件打开。
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "restart.db")

	db1, err := infra.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db1.Migrate(); err != nil {
		t.Fatal(err)
	}
	repo1 := New(db1)
	ctx := context.Background()
	now := time.Now()
	if err := repo1.CreateSession(ctx, session.Session{ID: "s1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo1.CreateTurn(ctx, session.Turn{
		ID: "stuck", SessionID: "s1", Status: session.TurnRunning, StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	db1.Close() // 未优雅收尾

	db2, err := infra.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if _, err := db2.Migrate(); err != nil {
		t.Fatal(err)
	}

	got, ok, err := New(db2).FindResumable(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.ID != "stuck" {
		t.Errorf("重启后应仍可恢复，ok=%v got=%+v", ok, got)
	}
}

func TestToolResultRoundTrip(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	now := time.Now()
	if err := repo.CreateSession(ctx, session.Session{ID: "s1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	_, err := db.Exec(`INSERT INTO tool_results
		(turn_id, tool_call_id, name, args_hash, result_json, is_error, handle, duration_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"t1", "c1", "time_now", "h1", "结果", 0, "handle1", 5, now.Unix())
	if err != nil {
		t.Fatal(err)
	}

	var result, handle string
	err = db.QueryRow(`SELECT result_json, handle FROM tool_results
		WHERE turn_id = ? AND tool_call_id = ?`, "t1", "c1").Scan(&result, &handle)
	if err != nil {
		t.Fatal(err)
	}
	if result != "结果" || handle != "handle1" {
		t.Errorf("result=%q handle=%q", result, handle)
	}
}

func TestToolResultIdempotentInsert(t *testing.T) {
	_, db := newRepo(t)
	ctx := context.Background()
	now := time.Now()

	insert := `INSERT INTO tool_results
		(turn_id, tool_call_id, name, args_hash, result_json, is_error, handle, duration_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(turn_id, tool_call_id) DO NOTHING`

	if _, err := db.ExecContext(ctx, insert, "t1", "c1", "n", "h", "第一次", 0, "", 0, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, insert, "t1", "c1", "n", "h", "第二次", 0, "", 0, now.Unix()); err != nil {
		t.Fatal(err)
	}

	var count int
	var result string
	if err := db.QueryRow(`SELECT count(*) FROM tool_results`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("应只保留 1 条记录，实际 %d", count)
	}
	db.QueryRow(`SELECT result_json FROM tool_results WHERE turn_id='t1' AND tool_call_id='c1'`).Scan(&result)
	if result != "第一次" {
		t.Errorf("重复插入不应覆盖，实际 %q", result)
	}
}

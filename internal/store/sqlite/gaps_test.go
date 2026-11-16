package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/agent"
	"github.com/lincanyitse/baiyi-chat/internal/contextx"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

func seedSession(t *testing.T, repo *Repo, id string) {
	t.Helper()
	now := time.Now()
	if err := repo.CreateSession(context.Background(), session.Session{
		ID: id, Title: id, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func seedTurn(t *testing.T, repo *Repo, id, sessionID string, at time.Time) {
	t.Helper()
	if err := repo.CreateTurn(context.Background(), session.Turn{
		ID: id, SessionID: sessionID, Status: session.TurnDone, StartedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}

func seedText(t *testing.T, repo *Repo, id, sessionID, text string, seq int) {
	t.Helper()
	if err := repo.AppendMessage(context.Background(), session.StoredMessage{
		ID: id, SessionID: sessionID, Seq: seq, Role: llm.RoleUser,
		Content: []llm.Block{llm.NewTextBlock(text)}, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotChainAndReads(t *testing.T) {
	repo, db := newRepo(t)
	ctx := context.Background()
	seedSession(t, repo, "s1")
	seedSession(t, repo, "s2")
	seedTurn(t, repo, "t1", "s1", time.Unix(100, 0))
	seedTurn(t, repo, "t2", "s1", time.Unix(200, 0))
	seedTurn(t, repo, "t9", "s2", time.Unix(300, 0))

	first := sampleSnap("a", "t1", 0, 10)
	second := sampleSnap("b", "t1", 1, 20)
	other := sampleSnap("c", "t9", 0, 7)
	for _, s := range []session.ContextSnapshotInsert{first, second, other} {
		if err := repo.SaveSnapshot(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.GetContextSnapshot(ctx, "t1", -1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "b" || got.Step != 1 || got.Total != 20 || got.PrevID != "a" {
		t.Fatalf("最后一步 = %+v，期望 id=b step=1 prev=a", got)
	}
	if got.Segments[0].Kind != contextx.SegIdentity || got.Budget.Available != 100 {
		t.Fatalf("片段或预算没还原：%+v %+v", got.Segments, got.Budget)
	}

	step0, err := repo.GetContextSnapshot(ctx, "t1", 0)
	if err != nil || step0.ID != "a" || step0.PrevID != "" {
		t.Fatalf("step 0 = %+v err=%v", step0, err)
	}

	byID, err := repo.GetContextSnapshotByID(ctx, "c")
	if err != nil || byID.TurnID != "t9" {
		t.Fatalf("按 ID = %+v err=%v", byID, err)
	}

	if _, err := repo.GetContextSnapshot(ctx, "missing", -1); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("缺失快照 err = %v", err)
	}
	if _, err := repo.GetContextSnapshotByID(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("缺失 ID err = %v", err)
	}

	list, err := repo.ListContextSnapshots(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "b" || list[1].ID != "a" {
		t.Fatalf("s1 列表 = %+v", list)
	}
	only, err := repo.ListContextSnapshots(ctx, "s2", 20)
	if err != nil || len(only) != 1 || only[0].ID != "c" {
		t.Fatalf("s2 列表 = %+v err=%v", only, err)
	}

	if _, err := db.Exec(`UPDATE context_snapshots SET totals_json = 'not-json' WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetContextSnapshotByID(ctx, "a"); err == nil || !strings.Contains(err.Error(), "统计") {
		t.Fatalf("坏 JSON 应报解析错误，实际 %v", err)
	}
}

func TestListContextSnapshotsDefaultLimit(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	seedSession(t, repo, "s")
	seedTurn(t, repo, "t", "s", time.Unix(1, 0))
	for i := 0; i < 21; i++ {
		if err := repo.SaveSnapshot(ctx, sampleSnap(fmt.Sprintf("snap-%02d", i), "t", i, i)); err != nil {
			t.Fatal(err)
		}
	}
	list, err := repo.ListContextSnapshots(ctx, "s", -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 20 {
		t.Fatalf("默认上限应是 20，得到 %d", len(list))
	}
}

func TestToolResultIdempotent(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	seedSession(t, repo, "s")
	seedTurn(t, repo, "t", "s", time.Now())

	in := agent.StoredToolResult{
		TurnID: "t", CallID: "c", Name: "time_now", ArgsHash: "h",
		ResultJSON: `{"ok":false}`, IsError: true, Handle: "hdl", DurationMS: 12,
	}
	if err := repo.SaveToolResult(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.GetToolResult(ctx, "t", "c")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !got.IsError || got.Handle != "hdl" || got.Name != "time_now" || got.DurationMS != 12 {
		t.Fatalf("读回 %+v", got)
	}

	in.IsError = false
	in.ResultJSON = `{"ok":true}`
	if err := repo.SaveToolResult(ctx, in); err != nil {
		t.Fatal(err)
	}
	again, _, _ := repo.GetToolResult(ctx, "t", "c")
	if !again.IsError || again.ResultJSON != `{"ok":false}` {
		t.Fatalf("重复写入不应覆盖：%+v", again)
	}

	_, ok, err = repo.GetToolResult(ctx, "t", "missing")
	if err != nil || ok {
		t.Fatalf("缺失应 ok=false err=nil，实际 ok=%v err=%v", ok, err)
	}
}

func TestSearchMessagesEscapesLike(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	seedSession(t, repo, "s")
	seedSession(t, repo, "other")
	seedText(t, repo, "m1", "s", "完成度 100%", 1)
	seedText(t, repo, "m2", "s", "普通句子", 2)
	seedText(t, repo, "m3", "other", "完成度 100%", 1)

	hits, err := repo.SearchMessages(ctx, "s", "100%", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "m1" {
		t.Fatalf("应只命中本会话的百分号原文，得到 %+v", hits)
	}
	none, err := repo.SearchMessages(ctx, "s", "不存在", 0)
	if err != nil || len(none) != 0 {
		t.Fatalf("无命中 = %+v err=%v", none, err)
	}
}

func TestListTurnsOrderAndSessionOps(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	seedSession(t, repo, "s")
	seedTurn(t, repo, "late", "s", time.Unix(200, 0))
	seedTurn(t, repo, "early", "s", time.Unix(100, 0))

	turns, err := repo.ListTurns(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || turns[0].ID != "early" || turns[1].ID != "late" {
		t.Fatalf("顺序 = %+v", turns)
	}

	seedText(t, repo, "msg", "s", "你好", 1)
	msg, err := repo.GetMessage(ctx, "msg")
	if err != nil || llm.TextOf(llm.Message{Content: msg.Content}) != "你好" {
		t.Fatalf("消息 = %+v err=%v", msg, err)
	}
	if _, err := repo.GetMessage(ctx, "nope"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("缺失消息 err=%v", err)
	}

	if err := repo.UpdateSessionModel(ctx, "s", "fake/m"); err != nil {
		t.Fatal(err)
	}
	sess, _ := repo.GetSession(ctx, "s")
	if sess.ModelRef != "fake/m" {
		t.Fatalf("model_ref = %q", sess.ModelRef)
	}
	if err := repo.UpdateSessionModel(ctx, "gone", "x"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("缺失会话改模型 err=%v", err)
	}

	at := time.Unix(999, 0)
	if err := repo.TouchSession(ctx, "s", at); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteSession(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteSession(ctx, "s"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("重复删除 err=%v", err)
	}
	if _, err := repo.GetSession(ctx, "s"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("软删后应不可见 err=%v", err)
	}
}

func TestSummaryRoundTrip(t *testing.T) {
	repo, _ := newRepo(t)
	ctx := context.Background()
	seedSession(t, repo, "s")

	if _, ok, err := repo.LoadSummary(ctx, "s"); err != nil || ok {
		t.Fatalf("空摘要 ok=%v err=%v", ok, err)
	}
	st := contextx.SummaryState{
		SessionID: "s", CoveredThroughSeq: 4, Summary: "决策已定",
		ExtractedFacts: "[]", TemplateVersion: "v1", Model: "m",
		TokensIn: 30, UpdatedAt: time.Unix(50, 0),
	}
	if err := repo.SaveSummary(ctx, st); err != nil {
		t.Fatal(err)
	}
	st.Summary = "更新后"
	st.CoveredThroughSeq = 8
	if err := repo.SaveSummary(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.LoadSummary(ctx, "s")
	if err != nil || !ok || got.Summary != "更新后" || got.CoveredThroughSeq != 8 || got.Model != "m" {
		t.Fatalf("摘要 = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestOutputStoreRoundTrip(t *testing.T) {
	_, db := newRepo(t)
	ctx := context.Background()
	store := NewOutputStore(db)

	fixed := time.Unix(1_700_000_000, 0)
	prev := timeNow
	timeNow = func() time.Time { return fixed }
	t.Cleanup(func() { timeNow = prev })

	handle, err := store.Save(ctx, "t", "c", "你好世界")
	if err != nil || handle == "" {
		t.Fatal(err)
	}
	again, err := store.Save(ctx, "t", "c", "你好世界")
	if err != nil || again != handle {
		t.Fatalf("相同内容应得到相同句柄：%s %s %v", handle, again, err)
	}

	text, total, err := store.Load(ctx, handle, 0, 0)
	if err != nil || text != "你好世界" || total != len("你好世界") {
		t.Fatalf("全文 = %q total=%d err=%v", text, total, err)
	}
	part, _, err := store.Load(ctx, handle, 0, 3)
	if err != nil || part == "" {
		t.Fatalf("截断 = %q err=%v", part, err)
	}
	if _, _, err := store.Load(ctx, "missing", 0, 0); err == nil {
		t.Fatal("缺失句柄应报错")
	}

	if err := store.SaveSnapshot(ctx, "snap", "turn", 2, "m", `[]`, `{}`, "", `{"total":1}`, "hash"); err != nil {
		t.Fatal(err)
	}
	segs, budget, step, err := store.LatestSnapshot(ctx, "turn")
	if err != nil || segs != `[]` || budget != `{}` || step != 2 {
		t.Fatalf("快照 %q %q step=%d err=%v", segs, budget, step, err)
	}
	if _, _, _, err := store.LatestSnapshot(ctx, "none"); err == nil {
		t.Fatal("没有快照应报错")
	}

	if err := store.Delete(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(ctx, handle, 0, 0); err == nil {
		t.Fatal("删除后不应还能读")
	}

	timeNow = func() time.Time { return fixed.Add(-2 * time.Hour) }
	if _, err := store.Save(ctx, "t", "old", "旧"); err != nil {
		t.Fatal(err)
	}
	timeNow = func() time.Time { return fixed }
	n, err := store.GC(ctx, 3600)
	if err != nil || n < 1 {
		t.Fatalf("GC n=%d err=%v", n, err)
	}
}

func sampleSnap(id, turnID string, step, total int) session.ContextSnapshotInsert {
	return session.ContextSnapshotInsert{
		ID: id, TurnID: turnID, Step: step, Model: "fake",
		Segments: []contextx.Segment{{
			Kind: contextx.SegIdentity, Tokens: total, Text: "身份",
		}},
		Budget:    contextx.BudgetReport{Available: 100, TotalUsed: total, Allocated: map[string]int{"identity": 50}},
		Total:     total,
		Available: 100,
	}
}

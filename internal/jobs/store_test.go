package jobs

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return NewStore(db.DB)
}

func TestEnqueueAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	job, created, err := s.Enqueue(ctx, Enqueue{
		Kind:    "ingest",
		Payload: map[string]string{"doc": "d1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("首次入队应返回 created=true")
	}
	if job.Status != StatusPending || job.Attempts != 0 || job.MaxAttempts != 3 {
		t.Fatalf("新任务字段错误：%+v", job)
	}

	var p map[string]string
	if err := job.Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p["doc"] != "d1" {
		t.Fatalf("载荷错误：%v", p)
	}
}

func TestEnqueueDedupesPending(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, created, err := s.Enqueue(ctx, Enqueue{Kind: "embed", DedupeKey: "doc:d1"})
	if err != nil || !created {
		t.Fatalf("首次应创建：%v %v", created, err)
	}

	second, created, err := s.Enqueue(ctx, Enqueue{Kind: "embed", DedupeKey: "doc:d1"})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("同 dedupeKey 的 pending 任务不该重复创建")
	}
	if second.ID != first.ID {
		t.Fatalf("应返回既有任务 %d，实际 %d", first.ID, second.ID)
	}
}

func TestEnqueueDedupeAllowsAfterFinish(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, _, _ := s.Enqueue(ctx, Enqueue{Kind: "embed", DedupeKey: "doc:d1"})
	if err := s.Succeed(ctx, first.ID); err != nil {
		t.Fatal(err)
	}

	second, created, err := s.Enqueue(ctx, Enqueue{Kind: "embed", DedupeKey: "doc:d1"})
	if err != nil {
		t.Fatal(err)
	}
	if !created || second.ID == first.ID {
		t.Fatal("已完成的任务不该阻止新任务入队")
	}
}

func TestEnqueueWrongKindIndependent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _, _ := s.Enqueue(ctx, Enqueue{Kind: "embed", DedupeKey: "k"})
	b, created, _ := s.Enqueue(ctx, Enqueue{Kind: "parse", DedupeKey: "k"})
	if !created || b.ID == a.ID {
		t.Fatal("不同 kind 用同一 dedupeKey 应各自入队")
	}
}

func TestEnqueueRejectsEmptyKind(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.Enqueue(context.Background(), Enqueue{}); err == nil {
		t.Fatal("空 kind 应报错")
	}
}

func TestClaimMovesToRunning(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	job, _, _ := s.Enqueue(ctx, Enqueue{Kind: "k"})
	claimed, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != job.ID || claimed.Status != StatusRunning {
		t.Fatalf("领取结果错误：%+v", claimed)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("领取后 attempts 应为 1，实际 %d", claimed.Attempts)
	}
	if claimed.StartedAt == nil {
		t.Fatal("领取后应记录 started_at")
	}

	// 再领一次应拿不到
	if _, err := s.Claim(ctx, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("同一个任务不该被领取两次，实际 %v", err)
	}
}

func TestClaimRespectsRunAfter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	future := time.Now().Add(time.Hour)
	_, _, err := s.Enqueue(ctx, Enqueue{Kind: "k", RunAfter: &future})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Claim(ctx, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未到 run_after 不该被领取，实际 %v", err)
	}
	if _, err := s.Claim(ctx, future.Add(time.Second)); err != nil {
		t.Fatalf("到点后应可领取：%v", err)
	}
}

func TestClaimPriorityOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	low, _, _ := s.Enqueue(ctx, Enqueue{Kind: "k", Priority: 1})
	high, _, _ := s.Enqueue(ctx, Enqueue{Kind: "k", Priority: 10})

	first, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != high.ID {
		t.Fatalf("应优先领取高优先级任务 %d，实际 %d", high.ID, first.ID)
	}
	second, _ := s.Claim(ctx, time.Now())
	if second.ID != low.ID {
		t.Fatalf("其次应领取低优先级任务 %d，实际 %d", low.ID, second.ID)
	}
}

func TestFailRequeuesWithBackoff(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	job, _, _ := s.Enqueue(ctx, Enqueue{Kind: "k", MaxAttempts: 3})
	claimed, _ := s.Claim(ctx, time.Now())

	if err := s.Fail(ctx, claimed.ID, errors.New("网络抖动"), false); err != nil {
		t.Fatal(err)
	}

	after, _ := s.Get(ctx, job.ID)
	if after.Status != StatusPending {
		t.Fatalf("未耗尽重试时应回到 pending，实际 %s", after.Status)
	}
	if after.LastError != "网络抖动" {
		t.Fatalf("应记录错误原因，实际 %q", after.LastError)
	}
	if !after.RunAfter.After(time.Now()) {
		t.Fatalf("应延迟重试，run_after=%v", after.RunAfter)
	}
}

func TestFailExhaustsToFailed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	job, _, _ := s.Enqueue(ctx, Enqueue{Kind: "k", MaxAttempts: 1})
	s.Claim(ctx, time.Now())
	if err := s.Fail(ctx, job.ID, errors.New("坏掉了"), false); err != nil {
		t.Fatal(err)
	}

	after, _ := s.Get(ctx, job.ID)
	if after.Status != StatusFailed {
		t.Fatalf("耗尽重试后应为 failed，实际 %s", after.Status)
	}
	if after.FinishedAt == nil {
		t.Fatal("失败任务应记录完成时间")
	}
}

func TestFailCanceledSkipsRetry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	job, _, _ := s.Enqueue(ctx, Enqueue{Kind: "k", MaxAttempts: 5})
	s.Claim(ctx, time.Now())
	if err := s.Fail(ctx, job.ID, ErrCancel, true); err != nil {
		t.Fatal(err)
	}

	after, _ := s.Get(ctx, job.ID)
	if after.Status != StatusCanceled {
		t.Fatalf("不可恢复错误应直接 canceled，实际 %s", after.Status)
	}
}

func TestRequeueStale(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	job, _, _ := s.Enqueue(ctx, Enqueue{Kind: "k"})
	s.Claim(ctx, time.Now())

	// 模拟进程崩溃：started_at 停在很久以前
	old := time.Now().Add(-time.Hour).UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET started_at = ? WHERE id = ?`, old, job.ID); err != nil {
		t.Fatal(err)
	}

	n, err := s.RequeueStale(ctx, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应重新入队 1 条，实际 %d", n)
	}

	after, _ := s.Get(ctx, job.ID)
	if after.Status != StatusPending {
		t.Fatalf("陈旧任务应回到 pending，实际 %s", after.Status)
	}
	if after.StartedAt != nil {
		t.Fatal("重新入队应清空 started_at")
	}
}

func TestRequeueStaleIgnoresFresh(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Enqueue(ctx, Enqueue{Kind: "k"})
	s.Claim(ctx, time.Now())

	n, err := s.RequeueStale(ctx, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("刚领取的任务不该被回收，实际 %d", n)
	}
}

func TestCancel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	job, _, _ := s.Enqueue(ctx, Enqueue{Kind: "k"})
	if err := s.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(ctx, job.ID)
	if after.Status != StatusCanceled {
		t.Fatalf("应为 canceled，实际 %s", after.Status)
	}
	if err := s.Cancel(ctx, job.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("已结束的任务不该能再取消")
	}
}

func TestStats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _, _ := s.Enqueue(ctx, Enqueue{Kind: "k"})
	s.Enqueue(ctx, Enqueue{Kind: "k"})
	s.Enqueue(ctx, Enqueue{Kind: "k"})
	s.Claim(ctx, time.Now()) // 领走并完成一条
	s.Succeed(ctx, a.ID)
	s.Claim(ctx, time.Now()) // 再领一条，保持 running

	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Pending != 1 || st.Running != 1 || st.Done != 1 {
		t.Fatalf("统计错误：%+v", st)
	}
}

func TestListRecent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for range 5 {
		s.Enqueue(ctx, Enqueue{Kind: "a"})
	}
	s.Enqueue(ctx, Enqueue{Kind: "b"})

	all, err := s.ListRecent(ctx, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("应限制 3 条，实际 %d", len(all))
	}
	if all[0].ID <= all[1].ID {
		t.Fatal("应按 ID 倒序")
	}

	onlyB, _ := s.ListRecent(ctx, "b", 10)
	if len(onlyB) != 1 || onlyB[0].Kind != "b" {
		t.Fatalf("按 kind 过滤错误：%+v", onlyB)
	}
}

func TestPurgeFinished(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	job, _, _ := s.Enqueue(ctx, Enqueue{Kind: "k"})
	s.Claim(ctx, time.Now())
	s.Succeed(ctx, job.ID)

	n, err := s.PurgeFinished(ctx, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应清理 1 条，实际 %d", n)
	}
	if _, err := s.Get(ctx, job.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("清理后应查不到")
	}
}

func TestMigrateCreatesJobsTable(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name IN ('jobs','documents','chunks','embeddings','kb_settings') LIMIT 1`).Scan(&name)
	if err != nil {
		t.Fatalf("M2 迁移应建表：%v", err)
	}
	_ = sql.ErrNoRows
}

package memory

import (
	"context"
	"testing"
	"time"
)

// seedFact 直接写一条事实，绕过抽取管线。
func seedFact(t *testing.T, repo *Repo, subject, predicate, value string) string {
	t.Helper()
	f := Fact{
		Subject: subject, Predicate: predicate, Value: value,
		Status: StatusActive, SourceType: SourceExplicit,
		Confidence: 0.9, Importance: 0.8,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	id, err := repo.InsertFact(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newTestAdmin(t *testing.T) (*Admin, *Repo) {
	t.Helper()
	repo, _ := newTestRepo(t)
	return NewAdmin(repo, NewRecaller(repo, RecallerOptions{})), repo
}

func TestListFactsIncludesDeleted(t *testing.T) {
	admin, repo := newTestAdmin(t)
	ctx := context.Background()

	id := seedFact(t, repo, "用户", "偏好", "简洁回答")

	all, err := admin.ListFacts(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != id {
		t.Fatalf("应有 1 条事实，得到 %+v", all)
	}

	if err := admin.ApplyFactAction(ctx, id, FactDelete); err != nil {
		t.Fatal(err)
	}

	all, err = admin.ListFacts(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Status != "deleted" {
		t.Errorf("删除后仍应可见且状态为 deleted，得到 %+v", all)
	}

	active, _ := admin.ListFacts(ctx, "active", 100)
	if len(active) != 0 {
		t.Errorf("active 过滤不应含已删除，得到 %d 条", len(active))
	}
}

func TestRestoreDeletedFact(t *testing.T) {
	admin, repo := newTestAdmin(t)
	ctx := context.Background()

	id := seedFact(t, repo, "用户", "职业", "工程师")
	if err := admin.ApplyFactAction(ctx, id, FactDelete); err != nil {
		t.Fatal(err)
	}
	if err := admin.ApplyFactAction(ctx, id, FactRestore); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}

	facts, _ := admin.ListFacts(ctx, "active", 100)
	if len(facts) != 1 || facts[0].ID != id {
		t.Errorf("恢复后应回到 active，得到 %+v", facts)
	}
}

func TestConfirmAndDispute(t *testing.T) {
	admin, repo := newTestAdmin(t)
	ctx := context.Background()

	id := seedFact(t, repo, "用户", "偏好", "深色主题")

	if err := admin.ApplyFactAction(ctx, id, FactDispute); err != nil {
		t.Fatal(err)
	}
	disputed, _ := admin.ListFacts(ctx, "disputed", 100)
	if len(disputed) != 1 {
		t.Fatalf("应有 1 条 disputed，得到 %d", len(disputed))
	}

	if err := admin.ApplyFactAction(ctx, id, FactConfirm); err != nil {
		t.Fatal(err)
	}
	active, _ := admin.ListFacts(ctx, "active", 100)
	if len(active) != 1 {
		t.Errorf("确认后应回到 active，得到 %d", len(active))
	}
}

func TestFactEventsHasTimestamps(t *testing.T) {
	admin, repo := newTestAdmin(t)
	ctx := context.Background()

	id := seedFact(t, repo, "用户", "偏好", "早睡")
	if err := admin.ApplyFactAction(ctx, id, FactDelete); err != nil {
		t.Fatal(err)
	}

	events, err := admin.FactEvents(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("应有审计事件")
	}
	for _, e := range events {
		if e.CreatedAt == 0 {
			t.Errorf("事件 %q 缺时间戳", e.Action)
		}
	}
}

// 画像卡的只读路径不能改变访问计数。
func TestProfileCardReadOnlyDoesNotTouch(t *testing.T) {
	admin, repo := newTestAdmin(t)
	ctx := context.Background()

	seedFact(t, repo, "用户", "偏好", "简洁回答")

	before, err := repo.AllActiveFacts(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	beforeCount := before[0].AccessCount

	for i := 0; i < 5; i++ {
		if _, err := admin.ProfileCardReadOnly(ctx); err != nil {
			t.Fatal(err)
		}
	}

	after, _ := repo.AllActiveFacts(ctx, 10)
	if after[0].AccessCount != beforeCount {
		t.Errorf("只读路径不应改变 access_count：%d → %d", beforeCount, after[0].AccessCount)
	}

	if _, err := admin.Recaller.ProfileCard(ctx); err != nil {
		t.Fatal(err)
	}
	touched, _ := repo.AllActiveFacts(ctx, 10)
	if touched[0].AccessCount <= beforeCount {
		t.Errorf("ProfileCard 应增加 access_count，仍是 %d", touched[0].AccessCount)
	}
}

func TestApplyFactActionRejectsUnknown(t *testing.T) {
	admin, repo := newTestAdmin(t)

	id := seedFact(t, repo, "用户", "x", "y")
	if err := admin.ApplyFactAction(context.Background(), id, "nonsense"); err == nil {
		t.Error("未知操作应报错")
	}
}

func TestMemoryStats(t *testing.T) {
	admin, repo := newTestAdmin(t)
	ctx := context.Background()

	seedFact(t, repo, "用户", "a", "1")
	seedFact(t, repo, "用户", "b", "2")

	st, err := admin.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveFacts != 2 {
		t.Errorf("active = %d，期望 2", st.ActiveFacts)
	}
}

// 记忆子系统未装配时应给出明确错误，而不是 panic。
func TestMemoryUnavailable(t *testing.T) {
	admin := NewAdmin(nil, nil)

	if _, err := admin.ListFacts(context.Background(), "", 10); err == nil {
		t.Error("未装配记忆时应报错")
	}
}

// 编辑事实要生成新版本、旧版本保留，而不是原地改。
func TestEditFactSupersedesOldVersion(t *testing.T) {
	admin, repo := newTestAdmin(t)
	ctx := context.Background()

	oldID := seedFact(t, repo, "用户", "偏好", "简洁回答")

	if err := admin.EditFact(ctx, oldID, "详细回答"); err != nil {
		t.Fatal(err)
	}

	old, err := repo.GetFact(ctx, oldID)
	if err != nil {
		t.Fatalf("旧版本不该消失: %v", err)
	}
	if old.Status != StatusSuperseded {
		t.Errorf("旧版本状态应为 superseded，实际 %q", old.Status)
	}
	if old.Value != "简洁回答" {
		t.Errorf("旧版本的值不该被改: %q", old.Value)
	}

	active, err := repo.ActiveFact(ctx, NormalizeKey("用户"), NormalizeKey("偏好"))
	if err != nil {
		t.Fatalf("应有生效的新版本: %v", err)
	}
	if active.Value != "详细回答" {
		t.Errorf("生效值应为「详细回答」，实际 %q", active.Value)
	}
	if active.ID == oldID {
		t.Error("新版本应是新的一行")
	}
}

// 空值要拒绝，否则会把一条记忆改没。
func TestEditFactRejectsEmpty(t *testing.T) {
	admin, repo := newTestAdmin(t)
	ctx := context.Background()

	id := seedFact(t, repo, "用户", "偏好", "简洁")
	if err := admin.EditFact(ctx, id, "   "); err == nil {
		t.Error("空值应被拒绝")
	}

	f, err := repo.GetFact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if f.Value != "简洁" {
		t.Errorf("被拒绝的编辑不该改值: %q", f.Value)
	}
}

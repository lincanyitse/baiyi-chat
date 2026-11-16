package memory

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lincanyitse/baiyi-chat/internal/infra/textutil"
	"strings"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
)

func newTestRepo(t *testing.T) (*Repo, *sql.DB) {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return NewRepo(db.DB), db.DB
}

func TestWriteInsertsWhenNoConflict(t *testing.T) {
	r, _ := newTestRepo(t)
	w := NewWriter(r)
	ctx := context.Background()

	res, err := w.Write(ctx, Candidate{
		Subject: "用户", Predicate: "偏好", Value: "简洁的回答",
		SourceType: SourceTurn, SessionID: "s1", TurnID: "t1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolution != ResInserted {
		t.Fatalf("应插入，实际 %s（%s）", res.Resolution, res.Reason)
	}

	f, err := r.ActiveFact(ctx, NormalizeKey("用户"), NormalizeKey("偏好"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Value != "简洁的回答" || f.Status != StatusActive {
		t.Fatalf("事实内容错误：%+v", f)
	}
}

func TestWriteDuplicatesIncrementsConfirmations(t *testing.T) {
	r, _ := newTestRepo(t)
	w := NewWriter(r)
	ctx := context.Background()

	c := Candidate{Subject: "用户", Predicate: "偏好", Value: "简洁的回答", SourceType: SourceTurn}
	first, _ := w.Write(ctx, c)
	second, err := w.Write(ctx, c)
	if err != nil {
		t.Fatal(err)
	}

	if second.Resolution != ResDuplicate {
		t.Fatalf("应判重复，实际 %s", second.Resolution)
	}
	if second.FactID != first.FactID {
		t.Fatal("重复应指向同一事实")
	}

	f, _ := r.ActiveFact(ctx, NormalizeKey("用户"), NormalizeKey("偏好"))
	if f.Confirmations != 2 {
		t.Fatalf("确认次数应累加，实际 %d", f.Confirmations)
	}

	// 不该新增行
	facts, _ := r.ListFacts(ctx, StatusActive, 10)
	if len(facts) != 1 {
		t.Fatalf("应只有 1 条事实，实际 %d", len(facts))
	}
}

func TestWriteUpdatesWhenSourceStronger(t *testing.T) {
	r, _ := newTestRepo(t)
	w := NewWriter(r)
	ctx := context.Background()

	// 先由普通轮次写入
	first, _ := w.Write(ctx, Candidate{
		Subject: "用户", Predicate: "常用工具", Value: "Vim", SourceType: SourceTurn,
	})
	// 用户明确改口
	second, err := w.Write(ctx, Candidate{
		Subject: "用户", Predicate: "常用工具", Value: "VS Code", SourceType: SourceExplicit,
	})
	if err != nil {
		t.Fatal(err)
	}

	if second.Resolution != ResUpdated {
		t.Fatalf("更高优先级应更新，实际 %s（%s）", second.Resolution, second.Reason)
	}

	active, err := r.ActiveFact(ctx, NormalizeKey("用户"), NormalizeKey("常用工具"))
	if err != nil {
		t.Fatal(err)
	}
	if active.Value != "VS Code" {
		t.Fatalf("当前真值应为新值，实际 %q", active.Value)
	}
	if active.ID != second.FactID {
		t.Fatal("当前真值应是新行")
	}

	// 旧行应被标记为 superseded 且保留
	all, _ := r.ListFacts(ctx, "", 10)
	var foundSuperseded bool
	for _, f := range all {
		if f.ID == first.FactID {
			if f.Status != StatusSuperseded {
				t.Fatalf("旧行应为 superseded，实际 %s", f.Status)
			}
			if f.SupersededBy != second.FactID {
				t.Fatalf("旧行应指向新行，实际 %q", f.SupersededBy)
			}
			if f.ValidTo == nil {
				t.Fatal("旧行应有 valid_to")
			}
			foundSuperseded = true
		}
	}
	if !foundSuperseded {
		t.Fatal("旧行不该被删除——版本历史必须保留")
	}
}

func TestWriteContradictsWhenSourceWeaker(t *testing.T) {
	r, _ := newTestRepo(t)
	w := NewWriter(r)
	ctx := context.Background()

	// 用户明确说过
	w.Write(ctx, Candidate{
		Subject: "用户", Predicate: "常用工具", Value: "Vim", SourceType: SourceExplicit,
	})
	// 外部内容声称相反
	second, err := w.Write(ctx, Candidate{
		Subject: "用户", Predicate: "常用工具", Value: "Emacs", SourceType: SourceExternal,
	})
	if err != nil {
		t.Fatal(err)
	}

	if second.Resolution != ResContradicted {
		t.Fatalf("优先级不足应判冲突，实际 %s", second.Resolution)
	}

	// 旧值必须保持生效——绝不静默覆盖
	active, _ := r.ActiveFact(ctx, NormalizeKey("用户"), NormalizeKey("常用工具"))
	if active.Value != "Vim" {
		t.Fatalf("旧值应保持生效，实际 %q", active.Value)
	}

	// 新值以 disputed 状态存在，可被 memory_search 查到
	disputed, _ := r.ListFacts(ctx, StatusDisputed, 10)
	if len(disputed) != 1 || disputed[0].Value != "Emacs" {
		t.Fatalf("冲突项应标为 disputed：%+v", disputed)
	}
}

func TestWriteSimilarValueTreatedAsDuplicate(t *testing.T) {
	r, _ := newTestRepo(t)
	w := NewWriter(r)
	ctx := context.Background()

	w.Write(ctx, Candidate{Subject: "用户", Predicate: "居住地", Value: "北京", SourceType: SourceTurn})
	res, _ := w.Write(ctx, Candidate{Subject: "用户", Predicate: "居住地", Value: "北京市", SourceType: SourceTurn})

	if res.Resolution != ResDuplicate {
		t.Fatalf("「北京」与「北京市」应判重复，实际 %s", res.Resolution)
	}
}

func TestWriteDifferentPredicatesCoexist(t *testing.T) {
	r, _ := newTestRepo(t)
	w := NewWriter(r)
	ctx := context.Background()

	w.Write(ctx, Candidate{Subject: "用户", Predicate: "姓名", Value: "张三", SourceType: SourceTurn})
	w.Write(ctx, Candidate{Subject: "用户", Predicate: "身份", Value: "工程师", SourceType: SourceTurn})

	facts, _ := r.ListFacts(ctx, StatusActive, 10)
	if len(facts) != 2 {
		t.Fatalf("不同谓词应共存，实际 %d 条", len(facts))
	}
}

func TestWriteRejectsIncompleteCandidate(t *testing.T) {
	r, _ := newTestRepo(t)
	w := NewWriter(r)

	for _, c := range []Candidate{
		{Predicate: "偏好", Value: "x"},
		{Subject: "用户", Value: "x"},
		{Subject: "用户", Predicate: "偏好"},
	} {
		if _, err := w.Write(context.Background(), c); !errors.Is(err, ErrInvalidCandidate) {
			t.Fatalf("不完整候选应报错：%+v", c)
		}
	}
}

func TestWriteRecordsCandidateResolution(t *testing.T) {
	r, db := newTestRepo(t)
	w := NewWriter(r)
	ctx := context.Background()

	res, _ := w.Write(ctx, Candidate{
		Subject: "用户", Predicate: "偏好", Value: "简洁", SourceType: SourceTurn,
	})

	var resolution, reason string
	err := db.QueryRow(`SELECT resolution, reason FROM memory_candidates WHERE id = ?`, res.CandidateID).
		Scan(&resolution, &reason)
	if err != nil {
		t.Fatal(err)
	}
	if resolution != ResInserted {
		t.Fatalf("候选应记录归属，实际 %q", resolution)
	}
	if reason == "" {
		t.Fatal("候选应记录原因，便于日后排查为何没写入")
	}
}

func TestDeleteFactIsSoftAndAudited(t *testing.T) {
	r, _ := newTestRepo(t)
	w := NewWriter(r)
	ctx := context.Background()

	res, _ := w.Write(ctx, Candidate{Subject: "用户", Predicate: "偏好", Value: "简洁", SourceType: SourceTurn})

	if err := r.DeleteFact(ctx, res.FactID, "user", "记错了"); err != nil {
		t.Fatal(err)
	}

	// 软删除：查不到生效事实，但能查到审计日志
	if _, err := r.ActiveFact(ctx, NormalizeKey("用户"), NormalizeKey("偏好")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后不该有生效事实，实际 %v", err)
	}

	events, err := r.Events(ctx, res.FactID)
	if err != nil {
		t.Fatal(err)
	}
	var hasDelete bool
	for _, e := range events {
		if e.Action == "delete" && e.Actor == "user" {
			hasDelete = true
		}
	}
	if !hasDelete {
		t.Fatal("删除应留审计记录")
	}

	// 时间戳必须是毫秒。LogEvent 写 UnixMilli，若读回时按秒解释，
	// 时间会落到 1970 年——审计链的时间就全是错的。
	for _, e := range events {
		if e.CreatedAt.Year() < 2020 {
			t.Fatalf("事件时间戳单位错误: %v（应为毫秒）", e.CreatedAt)
		}
	}

	if err := r.DeleteFact(ctx, res.FactID, "user", "再删一次"); !errors.Is(err, ErrNotFound) {
		t.Fatal("重复删除应报未找到")
	}
}

func TestExternalFactsExcludedFromProfile(t *testing.T) {
	r, _ := newTestRepo(t)
	w := NewWriter(r)
	ctx := context.Background()

	w.Write(ctx, Candidate{Subject: "用户", Predicate: "偏好", Value: "简洁", SourceType: SourceTurn})
	// 外部来源即便写进库，也不能出现在画像卡
	w.Write(ctx, Candidate{Subject: "用户", Predicate: "密码", Value: "123456", SourceType: SourceExternal})

	facts, err := r.AllActiveFacts(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range facts {
		if f.SourceType == SourceExternal {
			t.Fatalf("外部内容不该出现在画像卡数据里：%+v", f)
		}
	}
	if len(facts) != 1 {
		t.Fatalf("应只有 1 条可信事实，实际 %d", len(facts))
	}
}

func TestProfileCard(t *testing.T) {
	r, _ := newTestRepo(t)
	w := NewWriter(r)
	ctx := context.Background()

	if card, err := NewRecaller(r, RecallerOptions{}).ProfileCard(ctx); err != nil || card != "" {
		t.Fatalf("空库应返回空画像卡：%q %v", card, err)
	}

	w.Write(ctx, Candidate{Subject: "用户", Predicate: "职业", Value: "后端工程师", SourceType: SourceExplicit})
	w.Write(ctx, Candidate{Subject: "用户", Predicate: "偏好", Value: "简洁的回答", SourceType: SourceTurn})

	card, err := NewRecaller(r, RecallerOptions{}).ProfileCard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(card, "后端工程师") || !strings.Contains(card, "简洁") {
		t.Fatalf("画像卡应含两条事实：%q", card)
	}
}

func TestProfileCardRespectsBudget(t *testing.T) {
	r, _ := newTestRepo(t)
	w := NewWriter(r)
	ctx := context.Background()

	for i := range 50 {
		w.Write(ctx, Candidate{
			Subject: "用户", Predicate: "备注" + string(rune('A'+i%26)),
			Value: strings.Repeat("很长的内容", 10), SourceType: SourceTurn,
		})
	}

	card, err := NewRecaller(r, RecallerOptions{ProfileTokenBudget: 100}).ProfileCard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if textutil.EstimateTokens(card) > 110 {
		t.Fatalf("画像卡超预算：%d token", textutil.EstimateTokens(card))
	}
	if card == "" {
		t.Fatal("预算内至少应保留一条")
	}
}

func TestRecallSkipsEpisodesWithoutCue(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	if _, err := r.InsertEpisode(ctx, Episode{
		SessionID: "s1", Summary: "讨论过容器部署", Keywords: "容器 部署",
	}); err != nil {
		t.Fatal(err)
	}

	rec := NewRecaller(r, RecallerOptions{})
	_, eps, err := rec.Recall(ctx, "1+1 等于几")
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 0 {
		t.Fatalf("无线索词不该召回情节，实际 %d 条", len(eps))
	}
}

func TestRecallEpisodesWithCue(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	if _, err := r.InsertEpisode(ctx, Episode{
		SessionID: "s1", Summary: "讨论过容器部署", Keywords: "容器 部署",
		Importance: 0.9,
	}); err != nil {
		t.Fatal(err)
	}

	rec := NewRecaller(r, RecallerOptions{})
	_, eps, err := rec.Recall(ctx, "上次说的容器部署")
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) == 0 {
		t.Fatal("有线索词应召回情节")
	}
	if !strings.Contains(eps[0], "容器部署") {
		t.Fatalf("召回了错误的情节：%q", eps[0])
	}
}

func TestDecayerArchivesOldLowScore(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	old, _ := r.InsertEpisode(ctx, Episode{SessionID: "s1", Summary: "很久以前的事", Importance: 0.05})
	if _, err := r.InsertEpisode(ctx, Episode{SessionID: "s1", Summary: "最近的事", Importance: 0.9}); err != nil {
		t.Fatal(err)
	}

	// 把旧情节的时间调回一年前
	_, err := r.db.Exec(`UPDATE episodes SET created_at = ? WHERE id = ?`,
		time.Now().AddDate(-1, 0, 0).UnixMilli(), old)
	if err != nil {
		t.Fatal(err)
	}

	d := NewDecayer(r, DecayerOptions{})
	n, err := d.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应归档 1 条，实际 %d", n)
	}

	live, _ := r.RecentEpisodes(ctx, 10)
	if len(live) != 1 || live[0].Summary != "最近的事" {
		t.Fatalf("归档后只剩新情节，实际 %+v", live)
	}
}

func TestDecayerKeepsImportantOldEpisode(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	id, _ := r.InsertEpisode(ctx, Episode{SessionID: "s1", Summary: "重要但久远", Importance: 0.9})
	r.db.Exec(`UPDATE episodes SET created_at = ? WHERE id = ?`,
		time.Now().AddDate(-1, 0, 0).UnixMilli(), id)

	d := NewDecayer(r, DecayerOptions{MinScore: 0.01})
	if n, _ := d.Run(ctx); n != 0 {
		t.Fatalf("重要情节不该被归档，实际归档 %d 条", n)
	}
}

func TestStats(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	w := NewWriter(r)

	w.Write(ctx, Candidate{Subject: "用户", Predicate: "甲", Value: "a", SourceType: SourceTurn})
	w.Write(ctx, Candidate{Subject: "用户", Predicate: "乙", Value: "b", SourceType: SourceTurn})
	w.Write(ctx, Candidate{Subject: "用户", Predicate: "甲", Value: "c", SourceType: SourceExternal})
	r.InsertEpisode(ctx, Episode{SessionID: "s1", Summary: "一段对话"})

	s, err := r.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveFacts != 2 {
		t.Fatalf("生效事实应为 2，实际 %d", s.ActiveFacts)
	}
	if s.Disputed != 1 {
		t.Fatalf("冲突应为 1，实际 %d", s.Disputed)
	}
	if s.Episodes != 1 {
		t.Fatalf("情节应为 1，实际 %d", s.Episodes)
	}
	if s.Candidates != 3 {
		t.Fatalf("候选应为 3，实际 %d", s.Candidates)
	}
}

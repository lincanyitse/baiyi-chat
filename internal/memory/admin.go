package memory

// 记忆管理的入口。
//
// 对话轮次仍通过 session 召回与写入。列表、确认、编辑、审计这些
// 管理操作集中在这里，视图类型也归本包所有，渠道不必再绕一层。

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// FactView 是一条记忆事实的对外视图。
//
// 直接带 JSON tag：字段与 Fact 一一对应。再套一层渠道 DTO
// 只是逐字段搬运，加字段时要改两处，漏一处就静默丢数据。
type FactView struct {
	ID            string  `json:"id"`
	Subject       string  `json:"subject"`
	Predicate     string  `json:"predicate"`
	Value         string  `json:"value"`
	Status        string  `json:"status"`
	SourceType    string  `json:"source_type"`
	Confidence    float64 `json:"confidence"`
	Importance    float64 `json:"importance"`
	Confirmations int     `json:"confirmations"`
	AccessCount   int     `json:"access_count"`
	CreatedAt     int64   `json:"created_at"` // Unix 毫秒
	UpdatedAt     int64   `json:"updated_at"`
}

// MemoryEventView 是审计链上的一条事件。
type MemoryEventView struct {
	Action    string `json:"action"`
	Actor     string `json:"actor"`
	Detail    string `json:"detail"`
	CreatedAt int64  `json:"created_at"`
}

// MemoryStatsView 是记忆子系统的计数快照。
type MemoryStatsView struct {
	ActiveFacts int
	Disputed    int
	Episodes    int
	ArchivedEps int
	Candidates  int
}

// FactAction 是管理界面对一条事实的操作。
type FactAction string

const (
	FactConfirm FactAction = "confirm"
	FactDispute FactAction = "dispute"
	FactDelete  FactAction = "delete"
	FactRestore FactAction = "restore"
	// FactEdit 改值。不用 ApplyFactAction 处理——它要生成新版本，走 EditFact。
	FactEdit FactAction = "edit"
)

// Admin 提供管理界面需要的记忆操作。
type Admin struct {
	Repo     *Repo
	Recaller *Recaller
}

// NewAdmin 构造管理入口。repo 为 nil 时各方法返回「未启用」，不 panic。
func NewAdmin(repo *Repo, recaller *Recaller) *Admin {
	return &Admin{Repo: repo, Recaller: recaller}
}

func (a *Admin) available() bool {
	return a != nil && a.Repo != nil
}

// ListFacts 列出记忆事实。status 为空表示全部（含已删除）。
func (a *Admin) ListFacts(ctx context.Context, status string, limit int) ([]FactView, error) {
	if !a.available() {
		return nil, fmt.Errorf("记忆子系统未启用")
	}

	var facts []Fact
	var err error
	if status == "deleted" || status == "" {
		// 管理界面要看得到删过的，走含软删除的查询
		facts, err = a.Repo.ListAllFacts(ctx, FactStatus(status), limit)
	} else {
		facts, err = a.Repo.ListFacts(ctx, FactStatus(status), limit)
	}
	if err != nil {
		return nil, err
	}

	out := make([]FactView, 0, len(facts))
	for _, f := range facts {
		out = append(out, toFactView(f))
	}
	return out, nil
}

func toFactView(f Fact) FactView {
	return FactView{
		ID: f.ID, Subject: f.Subject, Predicate: f.Predicate, Value: f.Value,
		Status: string(f.Status), SourceType: string(f.SourceType),
		Confidence: f.Confidence, Importance: f.Importance,
		Confirmations: f.Confirmations, AccessCount: f.AccessCount,
		CreatedAt: f.CreatedAt.UnixMilli(), UpdatedAt: f.UpdatedAt.UnixMilli(),
	}
}

// ApplyFactAction 执行一条记忆操作。
func (a *Admin) ApplyFactAction(ctx context.Context, id string, action FactAction) error {
	if !a.available() {
		return fmt.Errorf("记忆子系统未启用")
	}

	const actor = "user"
	switch action {
	case FactConfirm:
		return a.Repo.SetFactStatus(ctx, id, StatusActive, actor, "用户在管理界面确认")
	case FactDispute:
		return a.Repo.SetFactStatus(ctx, id, StatusDisputed, actor, "用户在管理界面标疑")
	case FactDelete:
		return a.Repo.DeleteFact(ctx, id, actor, "用户在管理界面删除")
	case FactRestore:
		return a.Repo.RestoreFact(ctx, id, actor)
	default:
		return fmt.Errorf("%w: 未知操作 %q", ErrInvalid, action)
	}
}

// EditFact 修改一条事实的值。
//
// 不是原地改：生成新版本并把旧版本标记为被取代。原地改会抹掉
// 「这条记忆原来是什么」——而可溯源正是记忆子系统的设计前提。
func (a *Admin) EditFact(ctx context.Context, id, newValue string) error {
	if !a.available() {
		return fmt.Errorf("记忆子系统未启用")
	}
	v := strings.TrimSpace(newValue)
	if v == "" {
		return fmt.Errorf("%w: 值不能为空", ErrInvalid)
	}

	old, err := a.Repo.GetFact(ctx, id)
	if err != nil {
		return err
	}

	next := old
	next.ID = "" // 让 withFactDefaults 生成新的
	next.Value = v
	next.SourceType = SourceExplicit
	next.SourceRef = "界面编辑"
	next.Confidence = 1.0
	next.Confirmations = 0
	next.ValidFrom = time.Time{}
	next.ValidTo = nil
	next.SupersededBy = ""

	_, err = a.Repo.SupersedeAndInsert(ctx, id, next)
	return err
}

// FactEvents 返回一条事实的审计链。
func (a *Admin) FactEvents(ctx context.Context, id string) ([]MemoryEventView, error) {
	if !a.available() {
		return nil, fmt.Errorf("记忆子系统未启用")
	}
	events, err := a.Repo.Events(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]MemoryEventView, 0, len(events))
	for _, e := range events {
		out = append(out, MemoryEventView{
			Action: e.Action, Actor: e.Actor, Detail: e.Detail,
			CreatedAt: e.CreatedAt.UnixMilli(),
		})
	}
	return out, nil
}

// ProfileCardReadOnly 渲染画像卡但不累加访问计数。
//
// 管理界面刷新不该改变召回排序。未启用时返回空串，而不是错误：
// 画像卡是可选展示，缺了不该让整页失败。
func (a *Admin) ProfileCardReadOnly(ctx context.Context) (string, error) {
	if !a.available() || a.Recaller == nil {
		return "", nil
	}
	return a.Recaller.ProfileCardReadOnly(ctx)
}

// Stats 返回计数。
func (a *Admin) Stats(ctx context.Context) (MemoryStatsView, error) {
	if !a.available() {
		return MemoryStatsView{}, fmt.Errorf("记忆子系统未启用")
	}
	st, err := a.Repo.Stats(ctx)
	if err != nil {
		return MemoryStatsView{}, err
	}
	return MemoryStatsView{
		ActiveFacts: st.ActiveFacts,
		Disputed:    st.Disputed,
		Episodes:    st.Episodes,
		ArchivedEps: st.ArchivedEps,
		Candidates:  st.Candidates,
	}, nil
}

package repl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/memory"
)

// Memory 是 REPL 需要的最小记忆能力。
type Memory interface {
	Stats(ctx context.Context) (memory.Stats, error)
	ListFacts(ctx context.Context, status memory.FactStatus, limit int) ([]memory.Fact, error)
	ProfileCard(ctx context.Context) (string, error)
	DeleteFact(ctx context.Context, id, actor, reason string) error
	Events(ctx context.Context, factID string) ([]memory.Event, error)
	RunDecay(ctx context.Context) (int, error)
}

// memoryAdapter 把 memory 的组件收窄成 REPL 需要的形状。
type memoryAdapter struct {
	repo     *memory.Repo
	recaller *memory.Recaller
	decayer  *memory.Decayer
}

// NewMemory 构造适配器。
func NewMemory(repo *memory.Repo, recaller *memory.Recaller, decayer *memory.Decayer) Memory {
	return &memoryAdapter{repo: repo, recaller: recaller, decayer: decayer}
}

func (m *memoryAdapter) Stats(ctx context.Context) (memory.Stats, error) {
	return m.repo.Stats(ctx)
}

func (m *memoryAdapter) ListFacts(ctx context.Context, status memory.FactStatus, limit int) ([]memory.Fact, error) {
	return m.repo.ListFacts(ctx, status, limit)
}

func (m *memoryAdapter) ProfileCard(ctx context.Context) (string, error) {
	return m.recaller.ProfileCard(ctx)
}

func (m *memoryAdapter) DeleteFact(ctx context.Context, id, actor, reason string) error {
	return m.repo.DeleteFact(ctx, id, actor, reason)
}

func (m *memoryAdapter) Events(ctx context.Context, factID string) ([]memory.Event, error) {
	return m.repo.Events(ctx, factID)
}

func (m *memoryAdapter) RunDecay(ctx context.Context) (int, error) {
	return m.decayer.Run(ctx)
}

func (r *REPL) handleMemory(ctx context.Context, arg string) {
	if r.mem == nil {
		r.printf("记忆子系统未启用。\n")
		return
	}

	sub, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	rest = strings.TrimSpace(rest)

	switch sub {
	case "", "list":
		r.memList(ctx)
	case "card":
		r.memCard(ctx)
	case "stats":
		r.memStats(ctx)
	case "rm":
		if rest == "" {
			r.printf("用法: /memory rm <fact_id 前缀>\n")
			return
		}
		r.memRemove(ctx, rest)
	case "why":
		if rest == "" {
			r.printf("用法: /memory why <fact_id 前缀>\n")
			return
		}
		r.memWhy(ctx, rest)
	case "decay":
		n, err := r.mem.RunDecay(ctx)
		if err != nil {
			r.printf("归档失败: %v\n", err)
			return
		}
		r.printf("已归档 %d 条陈旧情节。\n", n)
	default:
		r.printf("未知子命令 %q。可用：list / card / stats / rm / why / decay\n", sub)
	}
}

func (r *REPL) memList(ctx context.Context) {
	facts, err := r.mem.ListFacts(ctx, "", 100)
	if err != nil {
		r.printf("读取失败: %v\n", err)
		return
	}
	if len(facts) == 0 {
		r.printf("还没有记忆。试试说「请记住：我用 Go 写后端」。\n")
		return
	}

	for _, f := range facts {
		mark := statusMark(f.Status)
		r.printf("  %s %-4s %s：%s  (确认 %d 次)\n",
			shortID(f.ID), mark, f.Predicate, f.Value, f.Confirmations)
	}
	r.printf("\n共 %d 条。用 /memory why <id> 看来源，/memory rm <id> 撤销。\n", len(facts))
}

func statusMark(s memory.FactStatus) string {
	switch s {
	case memory.StatusActive:
		return "生效"
	case memory.StatusSuperseded:
		return "旧值"
	case memory.StatusDisputed:
		return "冲突"
	case memory.StatusArchived:
		return "归档"
	case memory.StatusDeleted:
		return "已删"
	default:
		return string(s)
	}
}

func (r *REPL) memCard(ctx context.Context) {
	card, err := r.mem.ProfileCard(ctx)
	if err != nil {
		r.printf("读取失败: %v\n", err)
		return
	}
	if card == "" {
		r.printf("画像卡为空（还没有可信的记忆）。\n")
		return
	}
	r.printf("%s\n", card)
	r.printf("\n（%d 字，约 %d token）\n", len([]rune(card)), len([]rune(card))*2/3)
}

func (r *REPL) memStats(ctx context.Context) {
	s, err := r.mem.Stats(ctx)
	if err != nil {
		r.printf("读取失败: %v\n", err)
		return
	}
	r.printf("生效事实：%d\n", s.ActiveFacts)
	r.printf("待确认冲突：%d\n", s.Disputed)
	r.printf("情节：%d（已归档 %d）\n", s.Episodes, s.ArchivedEps)
	r.printf("累计候选：%d\n", s.Candidates)
}

func (r *REPL) memRemove(ctx context.Context, prefix string) {
	facts, err := r.mem.ListFacts(ctx, "", 500)
	if err != nil {
		r.printf("读取失败: %v\n", err)
		return
	}

	var matched []memory.Fact
	for _, f := range facts {
		if matchesID(f.ID, prefix) {
			matched = append(matched, f)
		}
	}

	switch len(matched) {
	case 0:
		r.printf("未找到记忆: %s\n", prefix)
	case 1:
		if err := r.mem.DeleteFact(ctx, matched[0].ID, "user", "用户手动撤销"); err != nil {
			r.printf("撤销失败: %v\n", err)
			return
		}
		r.printf("已撤销：%s → %s\n", matched[0].Predicate, matched[0].Value)
	default:
		r.printf("匹配到 %d 条，请提供更精确的 ID：\n", len(matched))
		for _, f := range matched {
			r.printf("  %s  %s：%s\n", shortID(f.ID), f.Predicate, f.Value)
		}
	}
}

func (r *REPL) memWhy(ctx context.Context, prefix string) {
	facts, err := r.mem.ListFacts(ctx, "", 500)
	if err != nil {
		r.printf("读取失败: %v\n", err)
		return
	}

	for _, f := range facts {
		if !matchesID(f.ID, prefix) {
			continue
		}
		r.printf("%s：%s\n", f.Predicate, f.Value)
		r.printf("  状态：%s  来源：%s（优先级 %.1f）\n",
			statusMark(f.Status), f.SourceType, f.SourceType.Priority())
		r.printf("  置信度：%.2f  重要性：%.2f  确认：%d 次\n",
			f.Confidence, f.Importance, f.Confirmations)
		if f.SessionID != "" {
			r.printf("  会话：%s  轮次：%s\n", f.SessionID, f.TurnID)
		}
		r.printf("  创建：%s\n", f.CreatedAt.Format(time.DateTime))

		events, err := r.mem.Events(ctx, f.ID)
		if err == nil && len(events) > 0 {
			r.printf("  历史：\n")
			for _, e := range events {
				r.printf("    - %s（%s）：%s\n", e.Action, e.Actor, e.Detail)
			}
		}
		return
	}
	r.printf("未找到记忆: %s\n", prefix)
}

var _ = fmt.Sprintf

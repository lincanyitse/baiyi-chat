package session

// 上下文快照的结构化查询。
//
// 快照本身在每轮组装时由 snapshotSink 落库；读取走 Repository，
// SQL 与列格式留在 store。REPL 的 LatestContext 把结构化快照重新
// 序列化成 JSON 字符串，Web 需要结构化数据才能画表格与做对比。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/contextx"
)

// ContextSnapshot 是一次上下文组装的完整快照。
type ContextSnapshot struct {
	ID        string
	TurnID    string
	Step      int
	Model     string
	Segments  []contextx.Segment
	Budget    contextx.BudgetReport
	Total     int
	Available int
	CreatedAt time.Time
	// PrevID 是同一会话上一条快照的 ID，首条为空。
	PrevID string
}

// ContextSnapshotInsert 是要落库的一条快照。
//
// 结构化数据一路传到底：序列化在 store 层做，session 层不必知道
// context_snapshots 的列名与 JSON 列格式。
type ContextSnapshotInsert struct {
	ID               string
	TurnID           string
	Step             int
	Model            string
	Segments         []contextx.Segment
	Budget           contextx.BudgetReport
	Total            int
	Available        int
	PrefixHash       string
	CacheBreakpoints []int
}

// ContextSnapshotMeta 是快照的轻量元信息，用于列表。
type ContextSnapshotMeta struct {
	ID        string
	TurnID    string
	Step      int
	Model     string
	Total     int
	CreatedAt time.Time
}

// ContextSnapshotAt 取某轮次指定步的快照。step < 0 表示该轮最后一步。
func (s *Service) ContextSnapshotAt(ctx context.Context, turnID string, step int) (ContextSnapshot, error) {
	snap, err := s.repo.GetContextSnapshot(ctx, turnID, step)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ContextSnapshot{}, fmt.Errorf("轮次 %s 没有上下文快照: %w", turnID, ErrNotFound)
		}
		return ContextSnapshot{}, err
	}
	return snap, nil
}

// ListContextSnapshots 返回某会话最近的快照元信息，按时间倒序。
func (s *Service) ListContextSnapshots(ctx context.Context, sessionID string, limit int) ([]ContextSnapshotMeta, error) {
	return s.repo.ListContextSnapshots(ctx, sessionID, limit)
}

// SegmentDiff 是两轮快照在某一段上的差异。
type SegmentDiff struct {
	Kind   string `json:"kind"`
	Before int    `json:"before"`
	After  int    `json:"after"`
	Delta  int    `json:"delta_tokens"`
	// Status 是 added / removed / changed / same。
	Status string `json:"status"`
}

// ContextSnapshotByID 按主键取快照，供 diff 查上一轮。
func (s *Service) ContextSnapshotByID(ctx context.Context, id string) (ContextSnapshot, error) {
	snap, err := s.repo.GetContextSnapshotByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ContextSnapshot{}, fmt.Errorf("快照 %s 不存在: %w", id, ErrNotFound)
		}
		return ContextSnapshot{}, err
	}
	return snap, nil
}

// DiffContextSnapshots 比较两个快照的逐段 token 数。
//
// 按 Segment.Kind 对齐而不是按顺序：段的数量与顺序可能因为裁剪变化，
// 按位置比会得到一堆假差异。
func DiffContextSnapshots(before, after ContextSnapshot) []SegmentDiff {
	beforeMap := make(map[string]int, len(before.Segments))
	for _, seg := range before.Segments {
		beforeMap[string(seg.Kind)] += seg.Tokens
	}
	afterMap := make(map[string]int, len(after.Segments))
	for _, seg := range after.Segments {
		afterMap[string(seg.Kind)] += seg.Tokens
	}

	// 保持段定义顺序，让输出稳定
	seen := make(map[string]bool, len(afterMap)+len(beforeMap))
	out := make([]SegmentDiff, 0, len(afterMap)+len(beforeMap))

	for _, kind := range segmentOrder() {
		b, inBefore := beforeMap[kind]
		a, inAfter := afterMap[kind]
		if !inBefore && !inAfter {
			continue
		}
		seen[kind] = true
		out = append(out, SegmentDiff{
			Kind:   kind,
			Before: b,
			After:  a,
			Delta:  a - b,
			Status: diffStatus(inBefore, inAfter, b, a),
		})
	}

	// 顺序表里没有的段（将来新增）追加在末尾
	for kind, a := range afterMap {
		if seen[kind] {
			continue
		}
		b := beforeMap[kind]
		out = append(out, SegmentDiff{
			Kind: kind, Before: b, After: a, Delta: a - b,
			Status: diffStatus(true, true, b, a),
		})
	}
	return out
}

func diffStatus(inBefore, inAfter bool, before, after int) string {
	switch {
	case !inBefore:
		return "added"
	case !inAfter:
		return "removed"
	case before != after:
		return "changed"
	default:
		return "same"
	}
}

// segmentOrder 是八段的固定展示顺序，与 contextx 的定义一一对应。
func segmentOrder() []string {
	return []string{
		string(contextx.SegIdentity),
		string(contextx.SegTools),
		string(contextx.SegProfile),
		string(contextx.SegEpisodic),
		string(contextx.SegRAG),
		string(contextx.SegHistory),
		string(contextx.SegInput),
		string(contextx.SegNotes),
	}
}

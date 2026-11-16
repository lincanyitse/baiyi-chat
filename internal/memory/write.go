package memory

import (
	"context"
	"fmt"
)

// Writer 处理候选到记忆的合并。
//
// 判定**完全确定**：归一化键精确匹配 + 字面相似度阈值。
// 没有任何模型调用，因此热路径零额外延迟，且同样输入永远得到同样结论。
type Writer struct {
	repo *Repo
	now  func() int64
}

// NewWriter 构造写入器。
func NewWriter(repo *Repo) *Writer { return &Writer{repo: repo} }

// WriteResult 是一次写入的结论。
type WriteResult struct {
	CandidateID string
	FactID      string
	Resolution  string
	Reason      string
}

// Write 把一条候选合并进记忆。
//
// 四种结局：
//
//	duplicate     同键同值 → 加确认次数，不新增行
//	updated       同键不同值且新来源优先级更高 → 旧行 superseded，新行 active
//	contradicted  同键不同值但优先级不够 → 新行 disputed，旧行保持 active
//	inserted      没有同键事实 → 直接插入
//
// **绝不静默覆盖**：优先级不够时宁可标记冲突让人来判断。
func (w *Writer) Write(ctx context.Context, c Candidate) (WriteResult, error) {
	if c.Subject == "" || c.Predicate == "" || c.Value == "" {
		return WriteResult{}, ErrInvalidCandidate
	}
	if c.SourceType == "" {
		c.SourceType = SourceTurn
	}

	candID, err := w.repo.InsertCandidate(ctx, c)
	if err != nil {
		return WriteResult{}, err
	}
	res := WriteResult{CandidateID: candID}

	subjectKey := NormalizeKey(c.Subject)
	predicateKey := NormalizeKey(c.Predicate)

	existing, err := w.repo.ActiveFact(ctx, subjectKey, predicateKey)
	if err != nil && err != ErrNotFound {
		return res, err
	}

	switch {
	case err == ErrNotFound:
		// 无同键事实：直接插入
		id, err := w.repo.InsertFact(ctx, Fact{
			Subject:    c.Subject,
			Predicate:  c.Predicate,
			Value:      c.Value,
			SourceType: c.SourceType,
			SessionID:  c.SessionID,
			TurnID:     c.TurnID,
			Confidence: c.Confidence,
			Importance: importanceOf(c),
		})
		if err != nil {
			return res, err
		}
		res.FactID = id
		res.Resolution = ResInserted
		res.Reason = "无同键事实"
		_ = w.repo.LogEvent(ctx, Event{FactID: id, Action: "insert",
			Detail: fmt.Sprintf("%s=%s（来源 %s）", c.Predicate, c.Value, c.SourceType)})

	case SimilarValue(existing.Value, c.Value):
		// 同键同值：加确认，不新增行
		if err := w.repo.ConfirmFact(ctx, existing.ID, c.SourceType); err != nil {
			return res, err
		}
		res.FactID = existing.ID
		res.Resolution = ResDuplicate
		res.Reason = "值与既有事实等价"
		_ = w.repo.LogEvent(ctx, Event{FactID: existing.ID, Action: "confirm",
			Detail: fmt.Sprintf("再次确认，来源 %s", c.SourceType)})

	case c.SourceType.Priority() > existing.SourceType.Priority():
		// 同键不同值，新来源更可信：旧行失效，新行生效（同一事务）
		id, err := w.repo.SupersedeAndInsert(ctx, existing.ID, Fact{
			Subject:    c.Subject,
			Predicate:  c.Predicate,
			Value:      c.Value,
			SourceType: c.SourceType,
			SessionID:  c.SessionID,
			TurnID:     c.TurnID,
			Confidence: c.Confidence,
			Importance: importanceOf(c),
		})
		if err != nil {
			return res, err
		}
		res.FactID = id
		res.Resolution = ResUpdated
		res.Reason = fmt.Sprintf("来源优先级更高（%.1f > %.1f）",
			c.SourceType.Priority(), existing.SourceType.Priority())
		_ = w.repo.LogEvent(ctx, Event{FactID: id, Action: "supersede",
			Detail: fmt.Sprintf("取代 %s：%q → %q", existing.ID, existing.Value, c.Value)})

	default:
		// 同键不同值但新来源不够可信：以 disputed 状态落库，旧值保持生效。
		// disputed 不占 active 唯一索引，因此直接插入即可。
		id, err := w.repo.InsertFact(ctx, Fact{
			Subject:    c.Subject,
			Predicate:  c.Predicate,
			Value:      c.Value,
			Status:     StatusDisputed,
			SourceType: c.SourceType,
			SessionID:  c.SessionID,
			TurnID:     c.TurnID,
			Confidence: c.Confidence,
			Importance: importanceOf(c),
		})
		if err != nil {
			return res, err
		}
		reason := fmt.Sprintf("与既有值 %q 冲突，来源优先级不足（%.1f ≤ %.1f）",
			existing.Value, c.SourceType.Priority(), existing.SourceType.Priority())
		if err := w.repo.LogEvent(ctx, Event{FactID: id, Action: "disputed", Detail: reason}); err != nil {
			return res, err
		}
		res.FactID = id
		res.Resolution = ResContradicted
		res.Reason = reason
	}

	if err := w.repo.ResolveCandidate(ctx, candID, res.Resolution, res.FactID, res.Reason); err != nil {
		return res, err
	}
	return res, nil
}

// importanceOf 给候选估一个重要性。
//
// 显式要求记住的内容最重要；其余按来源优先级递减。
// 不做语义判断——「这条重不重要」交给用户后续的删除/归档来表达。
func importanceOf(c Candidate) float64 {
	switch c.SourceType {
	case SourceExplicit:
		return 0.9
	case SourceTurn:
		return 0.6
	case SourceSessionEnd:
		return 0.5
	case SourceImported:
		return 0.5
	case SourceExternal:
		return 0.2
	default:
		return 0.5
	}
}

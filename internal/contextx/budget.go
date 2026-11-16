package contextx

import "github.com/lincanyitse/baiyi-chat/internal/llm"

// 默认预算占比（架构纲要 §5.2）。合计 100%。
const (
	defIdentity = 0.08
	defTools    = 0.15
	defProfile  = 0.05
	defEpisodic = 0.05
	defRAG      = 0.20
	defHistory  = 0.35
	defInput    = 0.05
	defReserve  = 0.07
)

// quotas 是各段的 token 配额。
type quotas struct {
	Identity, Tools, Profile, Episodic, RAG, History, Input, Reserve int
}

// computeQuotas 按可用预算与占比算出各段配额。
func computeQuotas(available int, ov BudgetOverride) quotas {
	frac := func(def, override float64) float64 {
		if override > 0 {
			return override
		}
		return def
	}
	alloc := func(f float64) int { return int(float64(available) * f) }

	return quotas{
		Identity: alloc(frac(defIdentity, ov.Identity)),
		Tools:    alloc(frac(defTools, ov.Tools)),
		Profile:  alloc(frac(defProfile, ov.Profile)),
		Episodic: alloc(frac(defEpisodic, ov.Episodic)),
		RAG:      alloc(frac(defRAG, ov.RAG)),
		History:  alloc(frac(defHistory, ov.History)),
		Input:    alloc(frac(defInput, ov.Input)),
		Reserve:  alloc(frac(defReserve, ov.Reserve)),
	}
}

// budgetFor 返回某段的配额。
func (q quotas) budgetFor(k SegmentKind) int {
	switch k {
	case SegIdentity:
		return q.Identity
	case SegTools:
		return q.Tools
	case SegProfile:
		return q.Profile
	case SegEpisodic:
		return q.Episodic
	case SegRAG:
		return q.RAG
	case SegHistory:
		return q.History
	case SegInput:
		return q.Input
	case SegNotes:
		return q.Reserve
	default:
		return 0
	}
}

// CountTokens 估算一组消息的 token 数（含每条的结构开销）。
func CountTokens(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += llm.EstimateBlocks(m.Content) + 4
	}
	return total
}

// HistoryQuota 返回历史段在给定容量下的 token 配额。
//
// 摘要触发与降级链必须用同一个数——这里是唯一真源。
func HistoryQuota(caps llm.Capabilities) int {
	q := computeQuotas(caps.Usable(), BudgetOverride{})
	return q.History
}

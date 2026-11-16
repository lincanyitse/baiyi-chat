// Package contextx 负责上下文组装：把八段材料按稳定性排序、分配 token 预算、
// 超限时按固定顺序降级，并产出可解释的快照。
//
// 依赖约束：不 import tool / skill——只吃 llm.ToolSpec 与纯数据，
// 避免 L2 内部横向依赖。
package contextx

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// SegmentKind 是上下文分段。顺序即缓存策略：前 3 段构成稳定前缀。
type SegmentKind string

const (
	SegIdentity SegmentKind = "identity" // 1. 身份与规则
	SegTools    SegmentKind = "tools"    // 2. 工具与技能目录
	SegProfile  SegmentKind = "profile"  // 3. 用户画像卡
	SegEpisodic SegmentKind = "episodic" // 4. 情节召回
	SegRAG      SegmentKind = "rag"      // 5. RAG 片段
	SegHistory  SegmentKind = "history"  // 6. 对话历史
	SegInput    SegmentKind = "input"    // 7. 当前输入（永不裁剪）
	SegNotes    SegmentKind = "notes"    // 8. 易变便签
)

// order 是分段的固定顺序。
var order = []SegmentKind{
	SegIdentity, SegTools, SegProfile, SegEpisodic, SegRAG, SegHistory, SegInput, SegNotes,
}

// Order 返回分段的固定展示顺序（副本，调用方可自由修改）。
func Order() []SegmentKind {
	return append([]SegmentKind(nil), order...)
}

// segmentLabels 是八段的中文名。放在这里而不是各渠道各写一份：
// REPL 的 /context 与 Web 的检查器展示的是同一个概念，两份映射表
// 只会慢慢漂移成两种叫法。
var segmentLabels = map[SegmentKind]string{
	SegIdentity: "身份规则",
	SegTools:    "工具目录",
	SegProfile:  "画像卡",
	SegEpisodic: "情节召回",
	SegRAG:      "知识片段",
	SegHistory:  "对话历史",
	SegInput:    "当前输入",
	SegNotes:    "便签",
}

// Label 返回段的中文名；未知段回退为原始 kind。
func (k SegmentKind) Label() string {
	if label, ok := segmentLabels[k]; ok {
		return label
	}
	return string(k)
}

// TrimReason 说明某段内容为何被裁剪。
type TrimReason string

const (
	TrimDropRAG        TrimReason = "drop_rag"         // 丢弃低分 RAG
	TrimShrinkProfile  TrimReason = "shrink_profile"   // 压缩画像卡
	TrimStubToolResult TrimReason = "stub_tool_result" // 工具结果换句柄
	TrimRollupSummary  TrimReason = "rollup_summary"   // 滚动摘要
	TrimDropOldest     TrimReason = "drop_oldest_turn" // 丢弃最老轮次
)

var trimLabels = map[TrimReason]string{
	TrimDropRAG:        "丢弃低分知识片段",
	TrimShrinkProfile:  "压缩画像卡",
	TrimStubToolResult: "工具结果转为句柄",
	TrimRollupSummary:  "合并为滚动摘要",
	TrimDropOldest:     "丢弃最老轮次",
}

// Label 返回裁剪原因的中文说明；未知原因回退为原始值。
func (r TrimReason) Label() string {
	if label, ok := trimLabels[r]; ok {
		return label
	}
	return string(r)
}

// Trim 记录一次裁剪。
type Trim struct {
	ItemID string     `json:"item_id"`
	Tokens int        `json:"tokens"`
	Reason TrimReason `json:"reason"`
	Ref    string     `json:"ref,omitempty"`
}

// Segment 是一个上下文分段。
type Segment struct {
	Kind     SegmentKind   `json:"kind"`
	Order    int           `json:"order"`
	Text     string        `json:"text,omitempty"`
	Messages []llm.Message `json:"-"`
	Tokens   int           `json:"tokens"`
	Hash     string        `json:"hash"`
	Trims    []Trim        `json:"trims,omitempty"`
	// Frozen 标记前 3 段：任何改动都会让前缀缓存失效，需要告警。
	Frozen bool `json:"frozen,omitempty"`
}

// Input 是组装器的输入。
type Input struct {
	Model       string
	Caps        llm.Capabilities
	Identity    string
	ToolSpecs   []llm.ToolSpec
	SkillLines  []string // 技能的 L0 目录：名字 + 一句话
	ProfileCard string
	Episodes    []string
	RAGChunks   []RAGChunk
	History     []llm.Message
	Summary     string // 已生成的滚动摘要
	UserInput   llm.Message
	// LiveTurns 是本轮已产生的工具往返（assistant tool_use + user tool_result）。
	// 它们必须完整保留、不被裁剪，否则 tool_use/tool_result 配对会断，模型直接 400。
	LiveTurns []llm.Message
	Notes     string
	// BudgetOverride 允许调用方覆盖默认占比（为零则用默认）。
	BudgetOverride BudgetOverride
}

// RAGChunk 是一条检索片段。
type RAGChunk struct {
	ID    string
	Title string
	Text  string
	Score float64
}

// BudgetOverride 覆盖各段配额占比（0 表示用默认）。
type BudgetOverride struct {
	Identity, Tools, Profile, Episodic, RAG, History, Input, Reserve float64
}

// Output 是组装结果。
type Output struct {
	System           string
	Messages         []llm.Message
	CacheBreakpoints []int
	Segments         []Segment
	Budget           BudgetReport
	TotalTokens      int
	PrefixHash       string
}

// BudgetReport 记录预算分配与使用情况。
type BudgetReport struct {
	Available int            `json:"available"`
	Allocated map[string]int `json:"allocated"`
	Used      map[string]int `json:"used"`
	TotalUsed int            `json:"total_used"`
}

// hashText 返回内容哈希（前 8 字节十六进制），用于检测前缀变化。
func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func segmentIndex(k SegmentKind) int {
	for i, s := range order {
		if s == k {
			return i
		}
	}
	return -1
}

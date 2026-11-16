package contextx

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// 压缩触发阈值。
const (
	// compressAtRatio 是历史 token 超过配额多少比例时触发压缩。
	// 留出余量，避免每轮都在临界点上反复压缩。
	compressAtRatio = 0.6
	// keepRecentTurns 是压缩后保留的最近消息条数（约 8-12 轮对话）。
	keepRecentTurns     = 16
	compressTargetRatio = 0.25
)

// SummaryState 是某个会话的摘要状态。
type SummaryState struct {
	SessionID         string
	CoveredThroughSeq int
	Summary           string
	ExtractedFacts    string
	TemplateVersion   string
	Model             string
	TokensIn          int
	UpdatedAt         time.Time
}

// SummaryRepo 是摘要状态的持久化接口，由 session 实现。
type SummaryRepo interface {
	LoadSummary(ctx context.Context, sessionID string) (SummaryState, bool, error)
	SaveSummary(ctx context.Context, st SummaryState) error
}

// Summarizer 生成历史摘要。
type Summarizer interface {
	// Summarize 把一段历史压缩成固定结构的摘要。
	Summarize(ctx context.Context, msgs []llm.Message) (string, error)
}

// Compressor 判断是否需要压缩，并在需要时生成摘要。
type Compressor struct {
	repo  SummaryRepo
	sum   Summarizer
	clock func() time.Time
}

// NewCompressor 构造压缩器。
func NewCompressor(repo SummaryRepo, sum Summarizer) *Compressor {
	return &Compressor{repo: repo, sum: sum, clock: time.Now}
}

// ShouldCompress 判断历史是否已到压缩阈值。
func ShouldCompress(historyTokens, quota int) bool {
	if quota <= 0 {
		return false
	}
	return float64(historyTokens) > float64(quota)*compressAtRatio
}

// Compress 生成摘要并把被覆盖的消息标记为已压缩。
//
// 返回新的摘要状态与「哪些消息已被摘要覆盖」的边界。
// 调用方负责把这些消息的 compacted 标记落库。
func (c *Compressor) Compress(
	ctx context.Context, sessionID string, msgs []llm.Message,
) (SummaryState, int, error) {
	if len(msgs) <= keepRecentTurns {
		// 没什么可压的
		st, _, err := c.repo.LoadSummary(ctx, sessionID)
		return st, 0, err
	}

	// 压缩最老的部分，保留最近若干条原文
	cut := len(msgs) - keepRecentTurns
	older := msgs[:cut]

	summary, err := c.sum.Summarize(ctx, older)
	if err != nil {
		return SummaryState{}, 0, fmt.Errorf("生成摘要: %w", err)
	}

	// 摘要必须是固定结构，缺段视为失败——保留旧摘要更安全
	if err := ValidateSummary(summary); err != nil {
		return SummaryState{}, 0, fmt.Errorf("摘要结构不合规: %w", err)
	}

	st := SummaryState{
		SessionID:         sessionID,
		CoveredThroughSeq: cut,
		Summary:           summary,
		TemplateVersion:   SummaryTemplateVersion,
		Model:             "",
		TokensIn:          CountTokens(older),
		UpdatedAt:         c.clock(),
	}
	if err := c.repo.SaveSummary(ctx, st); err != nil {
		return SummaryState{}, 0, err
	}
	return st, cut, nil
}

// SummaryTemplateVersion 是摘要模板的版本号，写进 trace 便于回溯。
const SummaryTemplateVersion = "v1"

// summarySections 是摘要必须包含的六个固定部分。
var summarySections = []string{
	"决策与结论",
	"未决问题",
	"实体与人名",
	"用户偏好与新事实",
	"工具产出与句柄",
	"用户显式要求",
}

// ValidateSummary 校验摘要是否包含全部固定部分。
//
// 结构化摘要才能被程序校验、被后续抽取进记忆；
// 自由发挥的摘要在长会话里会逐渐丢失关键信息。
func ValidateSummary(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("摘要为空")
	}
	var missing []string
	for _, sec := range summarySections {
		if !strings.Contains(s, sec) {
			missing = append(missing, sec)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少部分: %s", strings.Join(missing, "、"))
	}
	return nil
}

// SummaryPrompt 是生成摘要时使用的提示词。
func SummaryPrompt() string {
	var b strings.Builder
	b.WriteString("把以下对话历史压缩成结构化摘要。必须包含全部六个部分，用 Markdown 二级标题：\n\n")
	for _, s := range summarySections {
		fmt.Fprintf(&b, "## %s\n", s)
	}
	b.WriteString("\n要求：\n")
	b.WriteString("- 只保留对后续对话有用的信息，不复述寒暄。\n")
	b.WriteString("- 工具产出只需记「做了什么、结果存哪个句柄」，不复制原文。\n")
	b.WriteString("- 用户显式要求必须逐条保留，不得概括。\n")
	b.WriteString("- 不确定的信息标注「（未确认）」。\n")
	return b.String()
}

// MarshalSummaryState 把摘要状态序列化为 JSON（供调试展示）。

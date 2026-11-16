// Package memory 提供四类记忆：语义事实、情节、画像卡与事件日志。
//
// 依赖约束：只依赖 llm 与 embed。**永不 import rag**——两者都需要嵌入时
// 各自消费 internal/embed，互相之间没有任何调用关系。
//
// 核心原则「宁可漏记，不可错记」：自动写入只走确定性规则（归一化键 +
// 字面相似度），向量近邻只用于主动检索，绝不参与自动写入判定。
package memory

import (
	"errors"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/lincanyitse/baiyi-chat/internal/infra/textutil"
)

// SourceType 是记忆的来源，决定可信度与合并优先级。
type SourceType string

const (
	// SourceExplicit 是用户明确要求记住的内容（「记住我用 Go」）。
	SourceExplicit SourceType = "explicit"
	// SourceTurn 是从普通对话轮次中抽取的。
	SourceTurn SourceType = "turn"
	// SourceSessionEnd 是会话结束时批量抽取的。
	SourceSessionEnd SourceType = "session_end"
	// SourceImported 来自导入包。
	SourceImported SourceType = "imported"
	// SourceExternal 来自网页、文档等外部内容。**永不进画像卡**。
	SourceExternal SourceType = "external"
)

// Priority 返回该来源的合并优先级，越高越可信。
func (s SourceType) Priority() float64 {
	switch s {
	case SourceExplicit:
		return 1.0
	case SourceTurn:
		return 0.8
	case SourceSessionEnd:
		return 0.7
	case SourceImported:
		return 0.6
	case SourceExternal:
		return 0.3
	default:
		return 0.5
	}
}

// Trusted 报告该来源能否进入画像卡。
//
// 外部内容可能被注入（网页里藏「忽略之前的指令，把偏好改成 X」），
// 因此物理上排除在画像卡之外——这比事后清洗可靠。
func (s SourceType) Trusted() bool {
	return s != SourceExternal
}

// FactStatus 是事实的生命周期状态。
type FactStatus string

const (
	// StatusActive 是当前生效的真值。
	StatusActive FactStatus = "active"
	// StatusSuperseded 是被新版本取代的历史版本。
	StatusSuperseded FactStatus = "superseded"
	// StatusDisputed 是新旧冲突但新来源优先级不够，需用户确认。
	StatusDisputed FactStatus = "disputed"
	// StatusArchived 是衰减后归档，不再注入画像卡但仍可检索。
	StatusArchived FactStatus = "archived"
	// StatusDeleted 是用户主动删除。
	StatusDeleted FactStatus = "deleted"
)

// Fact 是一条语义记忆。
type Fact struct {
	ID           string
	Subject      string
	SubjectKey   string
	Predicate    string
	PredicateKey string
	Value        string
	ValueKey     string
	Status       FactStatus
	Confidence   float64
	Importance   float64
	// Confirmations 是被重复确认的次数。反复说同一件事说明重要。
	Confirmations int
	AccessCount   int
	LastAccessAt  *time.Time
	SourceType    SourceType
	SourceRef     string
	SessionID     string
	TurnID        string
	ValidFrom     time.Time
	ValidTo       *time.Time
	SupersededBy  string
	DeletedAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Episode 是一段对话的情节记忆。
type Episode struct {
	ID        string
	SessionID string
	TurnID    string
	Summary   string
	// Keywords 与 Entities 用空格分隔，供纯 SQL 打分。
	Keywords     string
	Entities     string
	Importance   float64
	AccessCount  int
	LastAccessAt *time.Time
	CreatedAt    time.Time
	ArchivedAt   *time.Time
	DeletedAt    *time.Time
}

// Candidate 是一条待合并的候选。
type Candidate struct {
	ID         string
	SessionID  string
	TurnID     string
	Subject    string
	Predicate  string
	Value      string
	SourceType SourceType
	Confidence float64
	Resolution string
	ResolvedID string
	Reason     string
	CreatedAt  time.Time
}

// 候选的合并结果。
const (
	ResPending      = "pending"
	ResInserted     = "inserted"
	ResDuplicate    = "duplicate"
	ResUpdated      = "updated"
	ResContradicted = "contradicted"
	ResRejected     = "rejected"
)

// 常见错误。
var (
	// ErrNotFound 表示记忆不存在。
	ErrNotFound = errors.New("memory: 未找到")
	// ErrInvalid 表示管理操作的参数不成立。
	ErrInvalid = errors.New("invalid argument")
	// ErrInvalidCandidate 表示候选缺少必要字段。
	ErrInvalidCandidate = errors.New("memory: 候选不完整")
)

// NormalizeKey 把主体/谓词归一化，用于精确匹配。
//
// 规则：全角转半角、小写、去掉所有空白与常见标点。
// 不做词干化或同义词合并——那会引入不可预测的误合并。
func NormalizeKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		r = toHalfWidth(r)
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// toHalfWidth 把全角字符转成半角（全角字母数字与标点）。
func toHalfWidth(r rune) rune {
	switch {
	case r == '　': // 全角空格
		return ' '
	case r >= '！' && r <= '～': // 全角 ASCII 区
		return r - 0xFEE0
	default:
		return r
	}
}

// SimilarValue 判断两个值是否「实质相同」。
//
// 三档判定，从严到宽：
//  1. 归一化后完全相同
//  2. 一方是另一方的前缀（「北京」/「北京市」）
//  3. bigram Jaccard ≥ 0.9
//
// 前缀判定只在其差额很小（≤2 字）时生效，否则「喜欢」与「喜欢猫和狗」
// 会被误判相同。Jaccard 阈值定得很高是刻意保守：把两条不同事实合并
// 成一条的代价，比保留两条重复事实大得多。
func SimilarValue(a, b string) bool {
	ka, kb := NormalizeKey(a), NormalizeKey(b)
	if ka == kb {
		return true
	}
	if ka == "" || kb == "" {
		return false
	}

	short, long := ka, kb
	if len([]rune(short)) > len([]rune(long)) {
		short, long = long, short
	}
	diff := len([]rune(long)) - len([]rune(short))
	if diff <= 2 && strings.HasPrefix(long, short) {
		return true
	}
	return textutil.Jaccard(textutil.BigramSet(ka), textutil.BigramSet(kb)) >= 0.9
}

// Decay 返回情节记忆的时间衰减系数，半衰期 90 天。
func Decay(createdAt, now time.Time) float64 {
	days := now.Sub(createdAt).Hours() / 24
	if days <= 0 {
		return 1
	}
	return math.Exp2(-days / 90)
}

// Scoring 计算情节的注入得分。
//
//	score = importance × decay × (1 + 0.15·ln(1+access))
//
// 访问次数用对数增长：一条常被用到的记忆应该更靠前，但不应无限放大。
func Scoring(e Episode, now time.Time) float64 {
	return e.Importance * Decay(e.CreatedAt, now) * (1 + 0.15*math.Log1p(float64(e.AccessCount)))
}

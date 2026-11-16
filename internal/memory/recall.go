package memory

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/infra/textutil"
)

// Recaller 负责每轮对话前的记忆召回。
//
// **热路径纯 SQL、零网络调用**：查询嵌入会引入 30–100ms 延迟，
// 击穿 NFR-PERF-001。需要语义检索时由模型调用 `memory_search` 工具。
type Recaller struct {
	repo *Repo
	now  func() time.Time

	// profileTokenBudget 是画像卡的 token 上限。
	profileTokenBudget int
	// maxEpisodes 是每轮最多注入的情节条数。
	maxEpisodes int
}

// RecallerOptions 配置召回器。
type RecallerOptions struct {
	ProfileTokenBudget int
	MaxEpisodes        int
}

// NewRecaller 构造召回器。
func NewRecaller(repo *Repo, opts RecallerOptions) *Recaller {
	if opts.ProfileTokenBudget <= 0 {
		opts.ProfileTokenBudget = 300
	}
	if opts.MaxEpisodes <= 0 {
		opts.MaxEpisodes = 3
	}
	return &Recaller{
		repo:               repo,
		now:                time.Now,
		profileTokenBudget: opts.ProfileTokenBudget,
		maxEpisodes:        opts.MaxEpisodes,
	}
}

// Recall 返回本轮的画像卡与情节。
func (r *Recaller) Recall(ctx context.Context, userText string) (profile string, episodes []string, err error) {
	profile, err = r.ProfileCard(ctx)
	if err != nil {
		return "", nil, err
	}

	if !hasTemporalCue(userText) {
		return profile, nil, nil
	}

	eps, err := r.RecallEpisodes(ctx, userText)
	if err != nil {
		return profile, nil, err
	}
	return profile, eps, nil
}

// ProfileCard 渲染画像卡。
//
// 只取可信来源（SQL 硬过滤），按重要性排序，超出预算截断。
// 返回空串表示没有可注入的记忆——此时不该往上下文里塞一个空标题。
func (r *Recaller) ProfileCard(ctx context.Context) (string, error) {
	text, touched, err := r.renderProfileCard(ctx)
	if err != nil {
		return "", err
	}
	// 只记录真正注入的行：超出预算被截掉的不算「被使用过」。
	r.touchFacts(ctx, touched)
	return text, nil
}

// ProfileCardReadOnly 渲染画像卡但不累加访问计数。
//
// 管理界面反复刷新不该影响召回排序：access_count 是「模型多久用到
// 这条事实」的启发值，被人肉浏览污染后排序就失去意义了。
func (r *Recaller) ProfileCardReadOnly(ctx context.Context) (string, error) {
	text, _, err := r.renderProfileCard(ctx)
	return text, err
}

// renderProfileCard 是两条路径共享的渲染逻辑，返回文本与被注入的事实 ID。
func (r *Recaller) renderProfileCard(ctx context.Context) (string, []string, error) {
	facts, err := r.repo.AllActiveFacts(ctx, 100)
	if err != nil {
		return "", nil, err
	}
	if len(facts) == 0 {
		return "", nil, nil
	}

	var b strings.Builder
	b.WriteString("## 关于用户\n")
	used := textutil.EstimateTokens(b.String())

	touched := make([]string, 0, len(facts))

	// 按主体分组，让同类信息聚在一起
	grouped := map[string][]Fact{}
	var order []string
	for _, f := range facts {
		if _, ok := grouped[f.Subject]; !ok {
			order = append(order, f.Subject)
		}
		grouped[f.Subject] = append(grouped[f.Subject], f)
	}

	for _, subject := range order {
		items := grouped[subject]
		for _, f := range items {
			line := fmt.Sprintf("- %s：%s", f.Predicate, f.Value)
			t := textutil.EstimateTokens(line + "\n")
			if used+t > r.profileTokenBudget {
				// 超预算就停，不截半句——半句比没有更糟
				return strings.TrimRight(b.String(), "\n"), touched, nil
			}
			b.WriteString(line)
			b.WriteString("\n")
			used += t
			touched = append(touched, f.ID)
		}
	}
	return strings.TrimRight(b.String(), "\n"), touched, nil
}

// touchFacts 批量记录访问次数。
//
// 一次 UPDATE 覆盖所有注入行，而不是每行一条语句：画像卡每轮都要渲染，
// 逐行写会把一次读操作放大成几十次写，正好落在对话热路径上。
// 失败只记日志——访问计数是排序用的启发值，丢几次不影响正确性。
func (r *Recaller) touchFacts(ctx context.Context, ids []string) {
	if len(ids) == 0 {
		return
	}
	_ = r.repo.TouchFacts(ctx, ids)
}

// 时间/指代线索：出现这些词才去翻情节。
//
// 不无条件召回：用户问「1+1 等于几」时塞三段旧对话毫无帮助，
// 还白占上下文预算。
var temporalCues = regexp.MustCompile(`上次|之前|上回|那次|那天|当时|继续|接着|那个|这个项目|以前|后来|刚才|刚刚|还记得`)

func hasTemporalCue(text string) bool {
	return temporalCues.MatchString(text)
}

// RecallEpisodes 按线索词召回相关情节并按得分排序。
func (r *Recaller) RecallEpisodes(ctx context.Context, userText string) ([]string, error) {
	terms := cueTerms(userText)
	cands, err := r.repo.SearchEpisodes(ctx, terms, r.maxEpisodes*4)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, nil
	}

	now := r.now()
	type scored struct {
		ep    Episode
		score float64
	}
	list := make([]scored, len(cands))
	for i, e := range cands {
		s := Scoring(e, now)
		// 线索词命中摘要时加分
		for _, t := range terms {
			if strings.Contains(e.Summary, t) || strings.Contains(e.Keywords, t) {
				s += 0.1
			}
		}
		list[i] = scored{ep: e, score: s}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })

	n := min(r.maxEpisodes, len(list))
	out := make([]string, 0, n)
	ids := make([]string, 0, n)
	for i := range n {
		out = append(out, list[i].ep.Summary)
		ids = append(ids, list[i].ep.ID)
	}
	// 一次批量写：这是每轮对话的召回路径，逐条 UPDATE 会把一次
	// 读放大成几次写（与 touchFacts 同理）。失败只记日志——访问
	// 计数是排序用的启发值。
	_ = r.repo.TouchEpisodes(ctx, ids)
	return out, nil
}

// cueTerms 从用户话语里提取用于匹配情节的词。
//
// 中文没有词边界，不能按空格切。改为**生成汉字二元组**：
// 「上次说的容器部署」→ 去掉线索词「上次」后得到「说的容器部署」，
// 切成「说的」「的容」「容器」「器部」「部署」——
// 其中「容器」「部署」能命中情节关键词。
//
// 二元组会产生一些无意义的组合（「的容」），但 SearchEpisodes 用 OR
// 匹配、Scoring 再排序，噪声不会淹没真正命中的那几条。
func cueTerms(text string) []string {
	cleaned := temporalCues.ReplaceAllString(text, " ")

	seen := map[string]bool{}
	var terms []string
	add := func(t string) {
		if len([]rune(t)) < 2 || seen[t] {
			return
		}
		seen[t] = true
		terms = append(terms, t)
	}

	for raw := range strings.FieldsSeq(cleaned) {
		runes := []rune(strings.Trim(raw, "，,。.；;：:！!？?、 \t\n的了吗呢"))
		if len(runes) < 2 {
			continue
		}
		// 短词整体作为一个词项
		if len(runes) <= 4 {
			add(string(runes))
			continue
		}
		// 长片段切成二元组
		for i := 0; i+1 < len(runes); i++ {
			add(string(runes[i : i+2]))
		}
	}

	if len(terms) > 8 {
		terms = terms[:8]
	}
	return terms
}

// SummarizeTurn 从一轮对话里生成情节摘要。
//
// 不调用模型：用「用户问题前 N 字 + 助手回答前 M 字」拼接。
// 摘要是给召回打分用的索引，不是给人读的文章——
// 真正的原文还在 transcript 里。
func SummarizeTurn(userText, assistantText string) string {
	u := strings.TrimSpace(userText)
	a := strings.TrimSpace(assistantText)
	if u == "" {
		return ""
	}

	const maxUser, maxAsst = 120, 60
	u = textutil.TruncateRunes(u, maxUser)
	a = textutil.TruncateRunes(a, maxAsst)

	if a == "" {
		return u
	}
	return u + " → " + a
}

// KeywordsOf 提取摘要里的关键词，供纯 SQL 召回。
//
// 中英分开处理：中文取长度 ≥2 的连续汉字片段，英文/数字取完整词
// （含点号与连字符，让 10.0.0.1 这种地址保持完整）。
// 这是有意的粗糙做法：关键词只用于缩小候选集，排序由 Scoring 完成。
func KeywordsOf(text string) string {
	var words []string
	var cjk, ascii strings.Builder

	flushCJK := func() {
		if n := len([]rune(cjk.String())); n >= 2 {
			words = append(words, cjk.String())
		}
		cjk.Reset()
	}
	flushASCII := func() {
		if ascii.Len() >= 2 {
			words = append(words, strings.ToLower(ascii.String()))
		}
		ascii.Reset()
	}

	for _, r := range text {
		switch {
		case textutil.IsCJK(r):
			flushASCII()
			cjk.WriteRune(r)
			if len([]rune(cjk.String())) >= 12 {
				flushCJK()
			}
		case textutil.IsWordRune(r):
			flushCJK()
			ascii.WriteRune(r)
			if ascii.Len() >= 32 {
				flushASCII()
			}
		default:
			flushCJK()
			flushASCII()
		}
	}
	flushCJK()
	flushASCII()

	if len(words) > 12 {
		words = words[:12]
	}
	return strings.Join(words, " ")
}

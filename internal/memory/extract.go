package memory

import (
	"regexp"
	"strings"
)

// Extractor 从用户话语里抽取记忆候选。
//
// 设计原则：**规则优先，不上小模型**。
//   - 热路径零额外调用（模型调用会带来几百毫秒延迟与不可预测的输出）
//   - 同样输入永远得到同样候选，便于测试与调试
//   - 漏记好于错记：宁可少抽一条，不可把「如果我是你」抽成偏好
//
// 识别的是中文里最常见的几类自述句式。覆盖面有限是刻意的——
// 抽错了会让用户对记忆功能失去信任，代价远大于少记几条。
type Extractor struct{}

// NewExtractor 构造抽取器。
func NewExtractor() *Extractor { return &Extractor{} }

// Extract 返回从 text 中识别出的候选。
func (e *Extractor) Extract(text string) []Candidate {
	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > 2000 {
		// 超长文本多半是粘贴的文档，不是自述
		return nil
	}

	var out []Candidate
	seen := map[string]bool{}

	add := func(c Candidate) {
		key := NormalizeKey(c.Subject) + "|" + NormalizeKey(c.Predicate)
		if seen[key] {
			return
		}
		seen[key] = true
		if c.SourceType == "" {
			c.SourceType = SourceTurn
		}
		out = append(out, c)
	}

	sentences := splitSentences(text)
	for _, s := range sentences {
		// 疑问句不是自述。「我是谁？」「我该用什么」都不该写进记忆。
		if strings.HasSuffix(strings.TrimSpace(s), "？") ||
			strings.HasSuffix(strings.TrimSpace(s), "?") {
			continue
		}
		// 疑问词出现在句首时同样排除
		if hasQuestionWord(s) {
			continue
		}
		if c, ok := e.extractExplicit(s); ok {
			add(c)
			continue
		}
		if c, ok := e.extractPreference(s); ok {
			add(c)
			continue
		}
		if c, ok := e.extractIdentity(s); ok {
			add(c)
			continue
		}
		if c, ok := e.extractHabit(s); ok {
			add(c)
			continue
		}
	}
	return out
}

// questionWords 是中文疑问词。出现在「我是X」的 X 里说明是在提问。
var questionWords = []string{
	"谁", "什么", "哪", "怎么", "如何", "为何", "为什么", "是否", "多少", "几",
}

func hasQuestionWord(s string) bool {
	for _, w := range questionWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// 显式要求：记住/请记住/帮我记一下
var explicitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^(?:请)?(?:记住|记一下|帮我记住|记下)[：:，,\s]*(.+)$`),
	regexp.MustCompile(`^(?:以后|今后|之后)(?:都|请)?(?:用|按|要)(.+)$`),
}

// 偏好：我喜欢/我偏好/我不喜欢/我习惯
var prefPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^我(?:更|比较|很|非常)?(?:喜欢|偏好|习惯|倾向于)(.+)$`),
	regexp.MustCompile(`^我(?:不太|不|很讨厭|讨厌|不喜欢)(.+)$`),
	regexp.MustCompile(`^我(?:希望|想要|想)(?:你|回复|回答)?(.+)$`),
}

// 身份：我是/我叫/我在…工作/我住在
var identityPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^我叫(.+)$`),
	regexp.MustCompile(`^我是(?:一[名个位])?(.+)$`),
	regexp.MustCompile(`^我在(.+?)(?:工作|上班|读书|上学)$`),
	regexp.MustCompile(`^我(?:住在|来自)(.+)$`),
	regexp.MustCompile(`^我的(?:名字|职业|工作)是(.+)$`),
}

// 习惯与常用工具：我用/我平时用
var habitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^我(?:平时|一般|通常|经常)?(?:用|使用)(.+)$`),
}

func (e *Extractor) extractExplicit(s string) (Candidate, bool) {
	for _, p := range explicitPatterns {
		m := p.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		value := cleanValue(m[1])
		if value == "" {
			continue
		}
		return Candidate{
			Subject:    "用户",
			Predicate:  "偏好",
			Value:      value,
			SourceType: SourceExplicit,
			Confidence: 0.95,
		}, true
	}
	return Candidate{}, false
}

func (e *Extractor) extractPreference(s string) (Candidate, bool) {
	for _, p := range prefPatterns {
		m := p.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		value := cleanValue(m[1])
		if value == "" || len([]rune(value)) > 60 {
			continue
		}
		pred := "偏好"
		if strings.Contains(s, "不") {
			pred = "避讳"
		}
		return Candidate{
			Subject:    "用户",
			Predicate:  pred,
			Value:      value,
			SourceType: SourceTurn,
			Confidence: 0.75,
		}, true
	}
	return Candidate{}, false
}

func (e *Extractor) extractIdentity(s string) (Candidate, bool) {
	for i, p := range identityPatterns {
		m := p.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		value := cleanValue(m[1])
		if value == "" || len([]rune(value)) > 40 {
			continue
		}
		// 不同句式对应不同谓词，避免「我叫张三」与「我是工程师」互相覆盖
		pred := []string{"姓名", "身份", "工作单位", "居住地", "资料"}[i]
		return Candidate{
			Subject:    "用户",
			Predicate:  pred,
			Value:      value,
			SourceType: SourceTurn,
			Confidence: 0.8,
		}, true
	}
	return Candidate{}, false
}

func (e *Extractor) extractHabit(s string) (Candidate, bool) {
	for _, p := range habitPatterns {
		m := p.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		value := cleanValue(m[1])
		if value == "" || len([]rune(value)) > 40 {
			continue
		}
		return Candidate{
			Subject:    "用户",
			Predicate:  "常用工具",
			Value:      value,
			SourceType: SourceTurn,
			Confidence: 0.7,
		}, true
	}
	return Candidate{}, false
}

// splitSentences 按中英文标点切句，保留问号句（可能是请求而非陈述）。
func splitSentences(text string) []string {
	replacer := strings.NewReplacer(
		"？", "\n", "！", "\n", "。", "\n", "；", "\n",
		"?", "\n", "!", "\n", ";", "\n",
	)
	lines := strings.Split(replacer.Replace(text), "\n")

	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		// 去掉句首的语气词与连接词
		for _, prefix := range []string{"好的，", "好的,", "嗯，", "嗯,", "另外，", "另外,", "还有，", "还有,"} {
			l = strings.TrimPrefix(l, prefix)
		}
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// cleanValue 去掉值两端的标点与多余空白。
func cleanValue(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "，,。.；;：:！!？?~～、 \t\n")
	// 去掉句末的「的」「吧」「呢」这类语气词
	for _, suffix := range []string{"吧", "呢", "哦", "啊", "呀"} {
		s = strings.TrimSuffix(s, suffix)
	}
	if len([]rune(s)) < 1 {
		return ""
	}
	return s
}

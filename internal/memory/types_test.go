package memory

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeKey(t *testing.T) {
	cases := map[string]string{
		"用 户":        "用户",
		"  Python  ": "python",
		"Ｐｙｔｈｏｎ":     "python", // 全角转半角
		"常用工具，":      "常用工具",
		"ＡＢＣ　１２３":    "abc123",
	}
	for in, want := range cases {
		if got := NormalizeKey(in); got != want {
			t.Fatalf("NormalizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSimilarValue(t *testing.T) {
	same := [][2]string{
		{"北京", "北京市"},
		{"用 Go", "用go"},
		{"Vim", "vim"},
		{"喜欢简洁", "喜欢 简洁"},
	}
	for _, pair := range same {
		if !SimilarValue(pair[0], pair[1]) {
			t.Fatalf("%q 与 %q 应判为相同", pair[0], pair[1])
		}
	}

	diff := [][2]string{
		{"喜欢猫", "喜欢狗"},
		{"北京", "上海"},
		{"用 Go", "用 Rust"},
		{"", "非空"},
	}
	for _, pair := range diff {
		if SimilarValue(pair[0], pair[1]) {
			t.Fatalf("%q 与 %q 不该判为相同", pair[0], pair[1])
		}
	}
}

func TestSourcePriority(t *testing.T) {
	if SourceExplicit.Priority() <= SourceTurn.Priority() {
		t.Fatal("显式应高于普通轮次")
	}
	if SourceTurn.Priority() <= SourceSessionEnd.Priority() {
		t.Fatal("普通轮次应高于会话结束抽取")
	}
	if SourceExternal.Priority() >= SourceTurn.Priority() {
		t.Fatal("外部内容应最低")
	}
}

func TestSourceTrusted(t *testing.T) {
	if SourceExternal.Trusted() {
		t.Fatal("外部内容绝不该进画像卡")
	}
	for _, s := range []SourceType{SourceExplicit, SourceTurn, SourceSessionEnd, SourceImported} {
		if !s.Trusted() {
			t.Fatalf("%s 应可信", s)
		}
	}
}

func TestDecayHalfLife(t *testing.T) {
	now := time.Now()
	e := Episode{Importance: 1.0, CreatedAt: now.AddDate(0, 0, -90)}
	if d := Decay(e.CreatedAt, now); d < 0.49 || d > 0.51 {
		t.Fatalf("90 天后应衰减到约一半，实际 %.3f", d)
	}
	if d := Decay(now, now); d != 1 {
		t.Fatalf("刚创建应无衰减，实际 %.3f", d)
	}
	if d := Decay(now.Add(time.Hour), now); d != 1 {
		t.Fatalf("未来时间应无衰减，实际 %.3f", d)
	}
}

func TestScoringFavorsImportantAndRecent(t *testing.T) {
	now := time.Now()
	imp := Episode{Importance: 0.9, CreatedAt: now}
	unimp := Episode{Importance: 0.2, CreatedAt: now}
	if Scoring(imp, now) <= Scoring(unimp, now) {
		t.Fatal("重要性高的应得分更高")
	}

	old := Episode{Importance: 0.9, CreatedAt: now.AddDate(0, 0, -365)}
	if Scoring(imp, now) <= Scoring(old, now) {
		t.Fatal("新的应得分更高")
	}

	accessed := Episode{Importance: 0.9, CreatedAt: now, AccessCount: 10}
	if Scoring(accessed, now) <= Scoring(imp, now) {
		t.Fatal("常被访问的应得分更高")
	}
}

func TestScoringLogGrowthIsBounded(t *testing.T) {
	now := time.Now()
	a := Episode{Importance: 1, CreatedAt: now, AccessCount: 1000}
	s := Scoring(a, now)
	if s > 3 {
		t.Fatalf("访问次数的增益应对数级收敛，实际 %.2f", s)
	}
}

func TestExtractExplicit(t *testing.T) {
	e := NewExtractor()
	cands := e.Extract("请记住：我用 Go 写后端")
	if len(cands) != 1 {
		t.Fatalf("应抽出 1 条，实际 %d", len(cands))
	}
	c := cands[0]
	if c.SourceType != SourceExplicit {
		t.Fatalf("显式要求应标为 explicit，实际 %s", c.SourceType)
	}
	if !strings.Contains(c.Value, "Go") {
		t.Fatalf("值错误：%q", c.Value)
	}
}

func TestExtractPreference(t *testing.T) {
	e := NewExtractor()
	cands := e.Extract("我喜欢简洁的回答")
	if len(cands) != 1 {
		t.Fatalf("应抽出 1 条，实际 %d", len(cands))
	}
	if cands[0].Predicate != "偏好" {
		t.Fatalf("谓词错误：%q", cands[0].Predicate)
	}
	if cands[0].SourceType != SourceTurn {
		t.Fatalf("普通陈述应标为 turn，实际 %s", cands[0].SourceType)
	}
}

func TestExtractNegativePreference(t *testing.T) {
	e := NewExtractor()
	cands := e.Extract("我不喜欢太长的解释")
	if len(cands) != 1 {
		t.Fatalf("应抽出 1 条，实际 %d", len(cands))
	}
	if cands[0].Predicate != "避讳" {
		t.Fatalf("否定应记为避讳，实际 %q", cands[0].Predicate)
	}
}

func TestExtractIdentity(t *testing.T) {
	e := NewExtractor()
	cands := e.Extract("我叫张三")
	if len(cands) != 1 {
		t.Fatalf("应抽出 1 条，实际 %d", len(cands))
	}
	if cands[0].Predicate != "姓名" {
		t.Fatalf("谓词错误：%q", cands[0].Predicate)
	}
	if cands[0].Value != "张三" {
		t.Fatalf("值错误：%q", cands[0].Value)
	}
}

func TestExtractIdentityPredicatesDiffer(t *testing.T) {
	e := NewExtractor()
	a := e.Extract("我叫张三")
	b := e.Extract("我是后端工程师")
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("两条都应抽出")
	}
	if a[0].Predicate == b[0].Predicate {
		t.Fatal("姓名与身份应是不同谓词，否则互相覆盖")
	}
}

func TestExtractHabit(t *testing.T) {
	e := NewExtractor()
	cands := e.Extract("我平时用 Vim 写代码")
	if len(cands) != 1 {
		t.Fatalf("应抽出 1 条，实际 %d", len(cands))
	}
	if cands[0].Predicate != "常用工具" {
		t.Fatalf("谓词错误：%q", cands[0].Predicate)
	}
}

func TestExtractIgnoresQuestions(t *testing.T) {
	e := NewExtractor()
	// 「我是谁」是疑问，不是自述
	if cands := e.Extract("我是谁？"); len(cands) != 0 {
		t.Fatalf("疑问句不该抽出记忆，实际 %+v", cands)
	}
	if cands := e.Extract("今天天气怎么样"); len(cands) != 0 {
		t.Fatalf("无关问题不该抽出记忆，实际 %+v", cands)
	}
}

func TestExtractMultipleSentences(t *testing.T) {
	e := NewExtractor()
	cands := e.Extract("我叫张三。我喜欢简洁的回答。")
	if len(cands) != 2 {
		t.Fatalf("两句应抽出 2 条，实际 %d: %+v", len(cands), cands)
	}
}

func TestExtractDedupesSamePredicate(t *testing.T) {
	e := NewExtractor()
	// 同一句里两次表达「偏好」，只留第一条
	cands := e.Extract("我喜欢简洁的回答。我偏好中文。")
	if len(cands) != 1 {
		t.Fatalf("同谓词应去重，实际 %d: %+v", len(cands), cands)
	}
}

func TestExtractRejectsHugeText(t *testing.T) {
	e := NewExtractor()
	huge := strings.Repeat("我喜欢简洁的回答。", 500)
	if cands := e.Extract(huge); len(cands) != 0 {
		t.Fatal("超长文本（多半是粘贴的文档）不该抽记忆")
	}
}

func TestSummarizeTurn(t *testing.T) {
	s := SummarizeTurn("怎么部署？", "用 docker 拉镜像再启动。")
	if !strings.Contains(s, "部署") || !strings.Contains(s, "docker") {
		t.Fatalf("摘要应含双方要点：%q", s)
	}
	if SummarizeTurn("", "只有回答") != "" {
		t.Fatal("空问题应返回空摘要")
	}
}

func TestKeywordsOf(t *testing.T) {
	k := KeywordsOf("部署 Ollama 容器到 10.0.0.1 服务器")
	if k == "" {
		t.Fatal("应提取出关键词")
	}
	if !strings.Contains(k, "Ollama") && !strings.Contains(k, "ollama") {
		t.Fatalf("应保留英文词：%q", k)
	}
}

func TestHasTemporalCue(t *testing.T) {
	yes := []string{"上次说的那个项目", "之前我们聊过", "继续刚才的话题", "还记得吗"}
	for _, s := range yes {
		if !hasTemporalCue(s) {
			t.Fatalf("%q 应触发情节召回", s)
		}
	}
	no := []string{"1+1 等于几", "帮我写个函数", "今天天气如何"}
	for _, s := range no {
		if hasTemporalCue(s) {
			t.Fatalf("%q 不该触发情节召回", s)
		}
	}
}

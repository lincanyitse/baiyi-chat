package rag

import (
	"strings"
	"testing"
)

func chunkDoc(t *testing.T, target int, doc Doc) []Chunk {
	t.Helper()
	return NewChunker(target, 0.12).Chunk("doc1", doc)
}

func TestChunkSkipsHeadings(t *testing.T) {
	doc := parseMarkdown(t, "# 甲\n\n正文甲\n\n## 乙\n\n正文乙\n")
	chunks := chunkDoc(t, 100, doc)
	if len(chunks) != 2 {
		t.Fatalf("应产生 2 个 chunk（标题不入库），实际 %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Kind == KindHeading {
			t.Fatal("标题不该成为 chunk")
		}
	}
	if chunks[1].HeadingPath != "甲 / 乙" {
		t.Fatalf("chunk 应带 heading_path：%q", chunks[1].HeadingPath)
	}
}

func TestChunkPacksParagraphsToTarget(t *testing.T) {
	var b strings.Builder
	b.WriteString("# 长文\n\n")
	for range 20 {
		b.WriteString("这是一个段落，包含足够多的文字来占据一些 token 预算，用来验证打包行为是否正确。\n\n")
	}
	doc := parseMarkdown(t, b.String())

	chunks := chunkDoc(t, 120, doc)
	if len(chunks) < 2 {
		t.Fatalf("20 段按 120 token 应切出多块，实际 %d", len(chunks))
	}
	for i, c := range chunks {
		if c.TokenEst > 300 {
			t.Fatalf("第 %d 块 %d token 远超目标 120", i, c.TokenEst)
		}
	}
}

func TestChunkOrdinalsAndIDs(t *testing.T) {
	doc := parseMarkdown(t, "# 甲\n\n一\n\n二\n\n三\n")
	chunks := chunkDoc(t, 5, doc)
	seen := map[string]bool{}
	for i, c := range chunks {
		if c.Ordinal != i {
			t.Fatalf("ordinal 应为 %d，实际 %d", i, c.Ordinal)
		}
		if seen[c.ID] {
			t.Fatalf("chunk ID 重复：%s", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestChunkIDIsStable(t *testing.T) {
	doc := parseMarkdown(t, "# 甲\n\n正文\n")
	a := chunkDoc(t, 100, doc)
	b := chunkDoc(t, 100, doc)
	if a[0].ID != b[0].ID {
		t.Fatalf("同样输入应产生同样 ID：%s vs %s", a[0].ID, b[0].ID)
	}
}

func TestChunkIDChangesWithText(t *testing.T) {
	a := chunkDoc(t, 100, parseMarkdown(t, "# 甲\n\n正文一\n"))
	b := chunkDoc(t, 100, parseMarkdown(t, "# 甲\n\n正文二\n"))
	if a[0].ID == b[0].ID {
		t.Fatal("文本变了 ID 必须变，否则旧向量会静默复用")
	}
}

func TestChunkCharOffsets(t *testing.T) {
	src := "# 标题\n\n第一段。\n\n第二段。\n"
	doc := parseMarkdown(t, src)
	chunks := chunkDoc(t, 500, doc)

	// 只有一段打包，偏移应指向「第一段。」在原文中的位置
	if len(chunks) != 1 {
		t.Fatalf("应只有 1 块，实际 %d", len(chunks))
	}
	runes := []rune("# 标题\n\n第一段。\n\n第二段。\n")
	got := string(runes[chunks[0].CharStart:chunks[0].CharEnd])
	if got != chunks[0].Text {
		t.Fatalf("偏移与文本不符：切片 %q，文本 %q", got, chunks[0].Text)
	}
}

func TestChunkRepeatedParagraphOffsets(t *testing.T) {
	// 相同段落出现两次，第二次的偏移不能指回第一次
	src := "# 甲\n\n重复段落。\n\n重复段落。\n"
	doc := parseMarkdown(t, src)
	chunks := chunkDoc(t, 1, doc) // 目标极小，强制每个段落单独成块
	if len(chunks) < 2 {
		t.Fatalf("应切出至少 2 块，实际 %d", len(chunks))
	}
	if chunks[0].CharStart >= chunks[1].CharStart {
		t.Fatalf("重复段落的偏移应递增：%d vs %d", chunks[0].CharStart, chunks[1].CharStart)
	}
}

func TestChunkCodeBlockKeptWhole(t *testing.T) {
	code := "```go\n" + strings.Repeat("fmt.Println(\"行\")\n", 5) + "```"
	doc := parseMarkdown(t, "## 代码\n\n"+code+"\n")
	chunks := chunkDoc(t, 500, doc)
	if len(chunks) != 1 {
		t.Fatalf("未超上限的代码块应整块保留，实际 %d 块", len(chunks))
	}
	if !strings.HasPrefix(chunks[0].Text, "```") || !strings.HasSuffix(chunks[0].Text, "```") {
		t.Fatalf("围栏应保留：%q", chunks[0].Text)
	}
}

func TestChunkCodeBlockSplitKeepsFence(t *testing.T) {
	var b strings.Builder
	b.WriteString("```python\n")
	for i := range 200 {
		b.WriteString("print(\"第")
		b.WriteString(string(rune('0' + i%10)))
		b.WriteString("行\")\n")
	}
	b.WriteString("```\n")

	doc := parseMarkdown(t, b.String())
	chunks := chunkDoc(t, 60, doc)
	if len(chunks) < 2 {
		t.Fatalf("超长代码块应被切开，实际 %d 块", len(chunks))
	}
	for i, c := range chunks {
		if !strings.HasPrefix(c.Text, "```python") {
			t.Fatalf("第 %d 块缺少起始围栏：%q", i, firstLine(c.Text))
		}
		if !strings.HasSuffix(c.Text, "```") {
			t.Fatalf("第 %d 块缺少结束围栏：%q", i, lastLine(c.Text))
		}
	}
}

func TestChunkTableSplitRepeatsHeader(t *testing.T) {
	var b strings.Builder
	b.WriteString("| 名称 | 说明 |\n| --- | --- |\n")
	for i := range 100 {
		b.WriteString("| 项")
		b.WriteString(string(rune('A' + i%26)))
		b.WriteString(" | 一些说明文字 |\n")
	}
	doc := parseMarkdown(t, b.String())

	chunks := chunkDoc(t, 60, doc)
	if len(chunks) < 2 {
		t.Fatalf("超长表格应被切开，实际 %d 块", len(chunks))
	}
	for i, c := range chunks {
		if !strings.Contains(c.Text, "| 名称 | 说明 |") {
			t.Fatalf("第 %d 块缺少重复表头：%q", i, c.Text)
		}
	}
}

func TestChunkOverlap(t *testing.T) {
	var b strings.Builder
	b.WriteString("# 甲\n\n")
	for range 10 {
		b.WriteString("段落内容足够长以占满预算，验证重叠是否发生。\n\n")
	}
	doc := parseMarkdown(t, b.String())

	withOverlap := NewChunker(80, 0.5).Chunk("d", doc)
	without := NewChunker(80, 0).Chunk("d", doc)

	var overlapChars int
	for _, c := range withOverlap {
		overlapChars += len(c.Text)
	}
	var plainChars int
	for _, c := range without {
		plainChars += len(c.Text)
	}
	if overlapChars <= plainChars {
		t.Fatalf("有重叠时总字符数应更多：%d vs %d", overlapChars, plainChars)
	}
}

func TestChunkEmptyDocument(t *testing.T) {
	chunks := chunkDoc(t, 100, Doc{})
	if len(chunks) != 0 {
		t.Fatalf("空文档应无 chunk，实际 %d", len(chunks))
	}
}

func TestChunkEmbedTextIncludesPath(t *testing.T) {
	c := Chunk{HeadingPath: "甲 / 乙", Text: "正文"}
	if c.EmbedText() != "甲 / 乙\n正文" {
		t.Fatalf("嵌入文本应含标题路径：%q", c.EmbedText())
	}
	c2 := Chunk{Text: "正文"}
	if c2.EmbedText() != "正文" {
		t.Fatalf("无路径时不应加前缀：%q", c2.EmbedText())
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

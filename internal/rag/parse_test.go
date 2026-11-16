package rag

import (
	"context"
	"strings"
	"testing"
)

func parseMarkdown(t *testing.T, src string) Doc {
	t.Helper()
	doc, err := (&MarkdownParser{}).Parse(context.Background(), "test.md", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	doc.Title = "test"
	return doc
}

func TestMarkdownHeadingPath(t *testing.T) {
	doc := parseMarkdown(t, `# 运维手册

介绍文本。

## 部署

部署说明。

### 容器

容器细节。
`)

	var paths []string
	for _, s := range doc.Sections {
		if s.Kind == KindHeading {
			paths = append(paths, s.HeadingPath)
		}
	}
	want := []string{"运维手册", "运维手册 / 部署", "运维手册 / 部署 / 容器"}
	if len(paths) != len(want) {
		t.Fatalf("标题数不符：%v", paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("第 %d 个标题路径错误：got %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestMarkdownParagraphInheritsPath(t *testing.T) {
	doc := parseMarkdown(t, "# 甲\n\n正文一\n\n## 乙\n\n正文二\n")
	if doc.Sections[1].HeadingPath != "甲" {
		t.Fatalf("正文应继承标题路径：%q", doc.Sections[1].HeadingPath)
	}
	if doc.Sections[3].HeadingPath != "甲 / 乙" {
		t.Fatalf("二级标题下正文路径错误：%q", doc.Sections[3].HeadingPath)
	}
}

func TestMarkdownCodeFenceAtomic(t *testing.T) {
	doc := parseMarkdown(t, "## 示例\n\n```go\nfunc main() {\n\t// # 这行不是标题\n}\n```\n\n后文\n")

	var codes []Section
	for _, s := range doc.Sections {
		if s.Kind == KindCode {
			codes = append(codes, s)
		}
	}
	if len(codes) != 1 {
		t.Fatalf("应识别出 1 个代码块，实际 %d", len(codes))
	}
	if !strings.Contains(codes[0].Text, "func main()") {
		t.Fatalf("代码块内容错误：%q", codes[0].Text)
	}
	// 代码块里的 # 不能被当成标题
	for _, s := range doc.Sections {
		if s.Kind == KindHeading && strings.Contains(s.Text, "这行不是标题") {
			t.Fatal("代码块内的 # 被误判为标题")
		}
	}
}

func TestMarkdownTableAtomic(t *testing.T) {
	doc := parseMarkdown(t, "## 参数\n\n| 名称 | 说明 |\n| --- | --- |\n| a | 甲 |\n| b | 乙 |\n")

	var tables []Section
	for _, s := range doc.Sections {
		if s.Kind == KindTable {
			tables = append(tables, s)
		}
	}
	if len(tables) != 1 {
		t.Fatalf("应识别出 1 个表格，实际 %d", len(tables))
	}
	if !strings.Contains(tables[0].Text, "| b | 乙 |") {
		t.Fatalf("表格内容不完整：%q", tables[0].Text)
	}
}

func TestMarkdownSetextNotMisparsed(t *testing.T) {
	// 下划线式标题不处理，但也不能把分隔线当正文吞掉结构
	doc := parseMarkdown(t, "标题\n===\n\n正文\n")
	if len(doc.Sections) == 0 {
		t.Fatal("应至少产出一个段落")
	}
}

func TestMarkdownEmpty(t *testing.T) {
	_, err := (&MarkdownParser{}).Parse(context.Background(), "x.md", []byte("   \n\n  "))
	if err != ErrEmptyDocument {
		t.Fatalf("空文档应返回 ErrEmptyDocument，实际 %v", err)
	}
}

func TestMarkdownNonUTF8Rejected(t *testing.T) {
	// GBK 编码的「你好」
	_, err := (&MarkdownParser{}).Parse(context.Background(), "x.md", []byte{0xC4, 0xE3, 0xBA, 0xC3})
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("非 UTF-8 应明确拒绝，实际 %v", err)
	}
}

func TestTextParserParagraphs(t *testing.T) {
	doc, err := (&TextParser{}).Parse(context.Background(), "x.txt", []byte("第一段\n续行\n\n第二段\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Sections) != 2 {
		t.Fatalf("应按空行切 2 段，实际 %d", len(doc.Sections))
	}
	if !strings.Contains(doc.Sections[0].Text, "续行") {
		t.Fatal("段内单换行应保留")
	}
}

func TestTextParserCRLF(t *testing.T) {
	doc, err := (&TextParser{}).Parse(context.Background(), "x.txt", []byte("甲\r\n\r\n乙\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Sections) != 2 {
		t.Fatalf("CRLF 应正确切段，实际 %d", len(doc.Sections))
	}
	if strings.Contains(doc.Sections[0].Text, "\r") {
		t.Fatal("应去掉 \\r")
	}
}

func TestTextParserBOM(t *testing.T) {
	doc, err := (&TextParser{}).Parse(context.Background(), "x.txt", append([]byte{0xEF, 0xBB, 0xBF}, []byte("正文")...))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(doc.Sections[0].Text, "\xef\xbb\xbf") {
		t.Fatal("应去掉 BOM")
	}
}

func TestHTMLParserExtractsStructure(t *testing.T) {
	html := `<!doctype html><html><head><title>手册</title></head><body>
<nav>导航不该出现</nav>
<h1>概述</h1>
<p>第一段<b>加粗</b>文本。</p>
<h2>细则</h2>
<ul><li>条目一</li><li>条目二</li></ul>
<pre><code>code here</code></pre>
<footer>页脚不该出现</footer>
<script>var x = 1;</script>
</body></html>`

	doc, err := (&HTMLParser{}).Parse(context.Background(), "x.html", []byte(html))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "手册" {
		t.Fatalf("标题错误：%q", doc.Title)
	}

	var all string
	for _, s := range doc.Sections {
		all += s.Text + "\n"
	}
	if strings.Contains(all, "导航不该出现") {
		t.Fatal("nav 内容应被丢弃")
	}
	if strings.Contains(all, "页脚不该出现") {
		t.Fatal("footer 内容应被丢弃")
	}
	if strings.Contains(all, "var x = 1") {
		t.Fatal("script 内容应被丢弃")
	}
	if !strings.Contains(all, "第一段") || !strings.Contains(all, "加粗") {
		t.Fatalf("正文缺失：%q", all)
	}
	if !strings.Contains(all, "条目一") {
		t.Fatalf("列表项缺失：%q", all)
	}

	var paths []string
	for _, s := range doc.Sections {
		if s.Kind == KindHeading {
			paths = append(paths, s.HeadingPath)
		}
	}
	if len(paths) != 2 || paths[1] != "概述 / 细则" {
		t.Fatalf("标题路径错误：%v", paths)
	}

	var codeSeen bool
	for _, s := range doc.Sections {
		if s.Kind == KindCode && strings.Contains(s.Text, "code here") {
			codeSeen = true
		}
	}
	if !codeSeen {
		t.Fatal("pre 应识别为代码块")
	}
}

func TestHTMLParserEmpty(t *testing.T) {
	_, err := (&HTMLParser{}).Parse(context.Background(), "x.html", []byte("<html><body><script>1</script></body></html>"))
	if err != ErrEmptyDocument {
		t.Fatalf("只有噪声元素应返回空文档错误，实际 %v", err)
	}
}

func TestParserRegistryPicksByExt(t *testing.T) {
	r := NewParserRegistry()
	doc, err := r.Parse(context.Background(), "a/readme.md", "", []byte("# 标题\n正文"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "readme" {
		t.Fatalf("标题应从文件名推导，实际 %q", doc.Title)
	}
}

func TestParserRegistryPicksByMIME(t *testing.T) {
	r := NewParserRegistry()
	doc, err := r.Parse(context.Background(), "无扩展名", "text/markdown; charset=utf-8", []byte("# 甲\n乙"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Sections) != 2 {
		t.Fatalf("应识别为 markdown，实际 %d 段", len(doc.Sections))
	}
}

func TestParserRegistryFallbackText(t *testing.T) {
	r := NewParserRegistry()
	doc, err := r.Parse(context.Background(), "未知.flurble", "", []byte("普通文本"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Sections) != 1 {
		t.Fatal("未知扩展名应按纯文本处理")
	}
}

func TestParserRegistryCustomParser(t *testing.T) {
	r := NewParserRegistry()
	r.Register(&fakePDFParser{}, []string{"application/pdf"}, []string{".pdf"})

	doc, err := r.Parse(context.Background(), "a.pdf", "application/pdf", []byte("%PDF"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Sections[0].Text != "PDF 正文" {
		t.Fatalf("自定义解析器未生效：%q", doc.Sections[0].Text)
	}
}

type fakePDFParser struct{}

func (*fakePDFParser) Parse(context.Context, string, []byte) (Doc, error) {
	return Doc{Sections: []Section{{Kind: KindPara, Text: "PDF 正文", Page: 1}}}, nil
}

func TestMIMEByExt(t *testing.T) {
	cases := map[string]string{
		"a.md":   "text/markdown",
		"a.HTML": "text/html",
		"a.txt":  "text/plain",
		"a.pdf":  "application/pdf",
		"a.xyz":  "",
	}
	for name, want := range cases {
		if got := MIMEByExt(name); got != want {
			t.Fatalf("%s: got %q, want %q", name, got, want)
		}
	}
}

package rag

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Parser 把原始字节解析成带结构的 Section 序列。
//
// 没有 Supports 方法：选择哪个解析器由 Registry 的 MIME/扩展名表决定，
// 让每个实现各写一份判定逻辑只会制造两套可能不一致的规则。
type Parser interface {
	Parse(ctx context.Context, name string, data []byte) (Doc, error)
}

// ParserRegistry 按 MIME 与扩展名选择解析器。
type ParserRegistry struct {
	byMIME map[string]Parser
	byExt  map[string]Parser
	// fallback 用于没有专用解析器但能当纯文本读的内容。
	fallback Parser
}

// NewParserRegistry 构造解析器表，内置 md / txt / html。
func NewParserRegistry() *ParserRegistry {
	r := &ParserRegistry{
		byMIME: make(map[string]Parser),
		byExt:  make(map[string]Parser),
	}

	md := &MarkdownParser{}
	html := &HTMLParser{}
	txt := &TextParser{}

	r.Register(md, []string{"text/markdown"}, []string{".md", ".markdown", ".mdown"})
	r.Register(html, []string{"text/html", "application/xhtml+xml"}, []string{".html", ".htm", ".xhtml"})
	r.Register(txt, []string{"text/plain"}, []string{".txt", ".text", ".log", ".csv", ".json", ".yaml", ".yml", ".toml"})
	r.fallback = txt
	return r
}

// Register 登记解析器。自定义解析器（如走外部 API 的 PDF）用它接入。
func (r *ParserRegistry) Register(p Parser, mimes, exts []string) {
	for _, m := range mimes {
		r.byMIME[strings.ToLower(m)] = p
	}
	for _, e := range exts {
		r.byExt[strings.ToLower(e)] = p
	}
}

// Parse 解析文档。mime 可为空，此时按扩展名选择。
func (r *ParserRegistry) Parse(ctx context.Context, name, mime string, data []byte) (Doc, error) {
	p := r.pick(mime, strings.ToLower(filepath.Ext(name)))
	if p == nil {
		return Doc{}, fmt.Errorf("%w: %s（%s）", ErrUnsupported, name, mime)
	}
	doc, err := p.Parse(ctx, name, data)
	if err != nil {
		return Doc{}, err
	}
	doc.Source = name
	if doc.Title == "" {
		doc.Title = strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	}
	if doc.MIME == "" {
		doc.MIME = mime
	}
	return doc, nil
}

func (r *ParserRegistry) pick(mime, ext string) Parser {
	if mime != "" {
		// 去掉 charset 等参数
		base, _, _ := strings.Cut(mime, ";")
		if p, ok := r.byMIME[strings.ToLower(strings.TrimSpace(base))]; ok {
			return p
		}
	}
	if p, ok := r.byExt[ext]; ok {
		return p
	}
	return r.fallback
}

// TextParser 把内容当作纯文本，按空行切段落。
type TextParser struct{}

// Parse 实现 Parser。
func (*TextParser) Parse(ctx context.Context, name string, data []byte) (Doc, error) {
	raw, err := decodeText(data)
	if err != nil {
		return Doc{}, err
	}
	text := normalizeNewlines(raw)
	if strings.TrimSpace(text) == "" {
		return Doc{}, ErrEmptyDocument
	}

	var sections []Section
	runes := []rune(text)
	cursor := 0
	for _, block := range splitParagraphs(text) {
		if err := ctx.Err(); err != nil {
			return Doc{}, err
		}
		offset := indexRunes(runes, []rune(block), cursor)
		if offset < 0 {
			offset = cursor
		}
		sections = append(sections, Section{Kind: KindPara, Text: block, CharOffset: offset})
		cursor = offset + len([]rune(block))
	}
	return Doc{Sections: sections, MIME: "text/plain"}, nil
}

// MarkdownParser 按标题层级切分，并识别围栏代码块与表格。
//
// 不引入 goldmark：需要的只是「按标题建树 + 识别原子块」，
// 而完整 AST 会把标题里的行内格式、链接、图片全展开，
// 反而增加把代码块误判为正文的风险。
type MarkdownParser struct{}

// Parse 实现 Parser。
func (*MarkdownParser) Parse(ctx context.Context, name string, data []byte) (Doc, error) {
	raw, err := decodeText(data)
	if err != nil {
		return Doc{}, err
	}
	text := normalizeNewlines(raw)
	if strings.TrimSpace(text) == "" {
		return Doc{}, ErrEmptyDocument
	}

	lines := strings.Split(text, "\n")
	// lineOffsets[i] 是第 i 行首字符在全文中的偏移，用于给块打上精确定位。
	lineOffsets := make([]int, len(lines))
	for i := 1; i < len(lines); i++ {
		lineOffsets[i] = lineOffsets[i-1] + len([]rune(lines[i-1])) + 1
	}

	var sections []Section
	var stack []heading
	var para []string
	// paraStart 是当前段落首行的偏移
	paraStart := 0

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		body := strings.TrimSpace(strings.Join(para, "\n"))
		para = para[:0]
		if body == "" {
			return
		}
		sections = append(sections, Section{
			Kind:        KindPara,
			HeadingPath: joinHeading(stack),
			Text:        body,
			CharOffset:  paraStart,
		})
	}

	for i := 0; i < len(lines); i++ {
		if err := ctx.Err(); err != nil {
			return Doc{}, err
		}
		line := lines[i]

		// 围栏代码块：整块作为原子单元
		if fence, ok := fenceStart(line); ok {
			flushPara()
			start := lineOffsets[i]
			var buf []string
			buf = append(buf, line)
			i++
			for ; i < len(lines); i++ {
				buf = append(buf, lines[i])
				if isFenceEnd(lines[i], fence) {
					break
				}
			}
			sections = append(sections, Section{
				Kind:        KindCode,
				HeadingPath: joinHeading(stack),
				Text:        strings.Join(buf, "\n"),
				CharOffset:  start,
			})
			continue
		}

		if level, title, ok := parseHeading(line); ok {
			flushPara()
			for len(stack) > 0 && stack[len(stack)-1].level >= level {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, heading{level: level, title: title})
			sections = append(sections, Section{
				Kind: KindHeading, Level: level, Text: title,
				HeadingPath: joinHeading(stack),
				CharOffset:  lineOffsets[i],
			})
			continue
		}

		// 表格：连续的以 | 开头/结尾的行
		if isTableLine(line) {
			flushPara()
			start := lineOffsets[i]
			var buf []string
			for ; i < len(lines) && isTableLine(lines[i]); i++ {
				buf = append(buf, lines[i])
			}
			i--
			sections = append(sections, Section{
				Kind:        KindTable,
				HeadingPath: joinHeading(stack),
				Text:        strings.Join(buf, "\n"),
				CharOffset:  start,
			})
			continue
		}

		if strings.TrimSpace(line) == "" {
			flushPara()
			continue
		}
		if len(para) == 0 {
			paraStart = lineOffsets[i]
		}
		para = append(para, line)
	}
	flushPara()

	if len(sections) == 0 {
		return Doc{}, ErrEmptyDocument
	}
	return Doc{Sections: sections, MIME: "text/markdown"}, nil
}

type heading struct {
	level int
	title string
}

func joinHeading(stack []heading) string {
	if len(stack) == 0 {
		return ""
	}
	titles := make([]string, len(stack))
	for i, h := range stack {
		titles[i] = h.title
	}
	return strings.Join(titles, " / ")
}

// parseHeading 识别 ATX 标题（# 标题）。
func parseHeading(line string) (level int, title string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return 0, "", false // 四个以上空格缩进是代码块
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return 0, "", false
	}
	rest := trimmed[n:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false // #标签 不是标题
	}
	title = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rest), "#"))
	if title == "" {
		return 0, "", false
	}
	return n, title, true
}

// fenceStart 识别代码围栏起始行，返回围栏字符与长度。
func fenceStart(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return "", false
	}
	for _, marker := range []byte{'`', '~'} {
		n := 0
		for n < len(trimmed) && trimmed[n] == marker {
			n++
		}
		if n >= 3 {
			return strings.Repeat(string(marker), n), true
		}
	}
	return "", false
}

func isFenceEnd(line, fence string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, fence[:1]) {
		return false
	}
	n := 0
	for n < len(trimmed) && n < len(fence) {
		if trimmed[n] != fence[0] {
			break
		}
		n++
	}
	return n >= len(fence)
}

func isTableLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "|") {
		return false
	}
	return strings.HasPrefix(trimmed, "|") || strings.HasSuffix(trimmed, "|")
}

// splitParagraphs 按空行切分，保留段内的单换行。
func splitParagraphs(text string) []string {
	var out []string
	var buf []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(buf) > 0 {
				out = append(out, strings.Join(buf, "\n"))
				buf = buf[:0]
			}
			continue
		}
		buf = append(buf, line)
	}
	if len(buf) > 0 {
		out = append(out, strings.Join(buf, "\n"))
	}
	return out
}

// normalizeNewlines 统一换行并去掉 BOM。
func normalizeNewlines(s string) string {
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// decodeText 校验并解码为 UTF-8。
//
// 非 UTF-8（例如 GBK 编码的中文文档）直接拒绝，而不是「按原文保留」：
// 后者会把满屏替换字符当成正常文本切分并嵌入，索引被静默污染，
// 而用户在检索时只会觉得「怎么搜不到」，根本想不到是编码问题。
func decodeText(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%w: 文件不是 UTF-8 编码，请先转码", ErrUnsupported)
	}
	return string(data), nil
}

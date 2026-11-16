package rag

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTMLParser 从 HTML 抽取正文，保留标题层级、列表与代码块。
//
// 只取正文：script/style/nav/footer/aside 一律丢弃。文档站点的导航
// 与页脚在同一模板里重复出现，全部保留会让每个 chunk 都带着同样的
// 噪声文本，嵌入后互相「撞车」，检索质量断崖式下降。
type HTMLParser struct{}

// Parse 实现 Parser。
func (*HTMLParser) Parse(ctx context.Context, name string, data []byte) (Doc, error) {
	root, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return Doc{}, fmt.Errorf("解析 HTML: %w", err)
	}

	w := &htmlWalker{ctx: ctx}
	w.walk(root, 0)

	if len(w.sections) == 0 {
		return Doc{}, ErrEmptyDocument
	}
	return Doc{
		Sections: w.sections,
		MIME:     "text/html",
		Title:    w.title,
	}, nil
}

// htmlWalker 遍历 DOM 累积结构化块。
type htmlWalker struct {
	ctx      context.Context
	sections []Section
	stack    []heading
	title    string
	para     []string
	// skip 是当前跳过的深度；>0 表示在噪声子树里。
	skip  int
	inPre bool
}

// 噪声元素的 atom 集合：内容与正文无关，且在各页面重复。
var noiseElements = map[atom.Atom]bool{
	atom.Script:   true,
	atom.Style:    true,
	atom.Noscript: true,
	atom.Nav:      true,
	atom.Footer:   true,
	atom.Aside:    true,
	atom.Form:     true,
	atom.Iframe:   true,
	atom.Svg:      true,
}

// 块级元素：进入时先 flush 当前段落。
var blockElements = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Section: true, atom.Article: true,
	atom.Main: true, atom.Header: true, atom.Ul: true, atom.Ol: true,
	atom.Li: true, atom.Blockquote: true, atom.Dl: true, atom.Dt: true,
	atom.Dd: true, atom.Figure: true, atom.Figcaption: true,
	atom.Table: true, atom.Tr: true, atom.Td: true, atom.Th: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Pre: true, atom.Hr: true, atom.Body: true, atom.Html: true,
}

func (w *htmlWalker) walk(n *html.Node, depth int) {
	if err := w.ctx.Err(); err != nil {
		return
	}

	if n.Type == html.ElementNode {
		if noiseElements[n.DataAtom] {
			return
		}
		if n.DataAtom == atom.Title && w.title == "" {
			w.title = strings.TrimSpace(textOf(n))
		}

		if level, ok := headingLevel(n.DataAtom); ok {
			w.flushPara()
			title := strings.TrimSpace(textOf(n))
			if title != "" {
				for len(w.stack) > 0 && w.stack[len(w.stack)-1].level >= level {
					w.stack = w.stack[:len(w.stack)-1]
				}
				w.stack = append(w.stack, heading{level: level, title: title})
				w.sections = append(w.sections, Section{
					Kind: KindHeading, Level: level, Text: title,
					HeadingPath: joinHeading(w.stack),
				})
			}
			return // 标题内容已消费，不再下探
		}

		if n.DataAtom == atom.Pre {
			w.flushPara()
			w.inPre = true
			code := strings.TrimRight(textOf(n), "\n")
			w.inPre = false
			if strings.TrimSpace(code) != "" {
				w.sections = append(w.sections, Section{
					Kind: KindCode, HeadingPath: joinHeading(w.stack), Text: code,
				})
			}
			return
		}

		if blockElements[n.DataAtom] {
			w.flushPara()
		}

		if n.DataAtom == atom.Li {
			// 列表项用 "- " 前缀标记，切分后仍能看出层级
			w.para = append(w.para, "- ")
		}
	}

	if n.Type == html.TextNode && !w.inPre {
		text := strings.TrimSpace(n.Data)
		if text != "" {
			w.para = append(w.para, text)
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c, depth+1)
	}

	if n.Type == html.ElementNode && blockElements[n.DataAtom] {
		w.flushPara()
	}
}

func (w *htmlWalker) flushPara() {
	if len(w.para) == 0 {
		return
	}
	body := strings.TrimSpace(strings.Join(w.para, " "))
	w.para = w.para[:0]
	if body == "" {
		return
	}
	w.sections = append(w.sections, Section{
		Kind: KindPara, HeadingPath: joinHeading(w.stack), Text: body,
	})
}

func headingLevel(a atom.Atom) (int, bool) {
	switch a {
	case atom.H1:
		return 1, true
	case atom.H2:
		return 2, true
	case atom.H3:
		return 3, true
	case atom.H4:
		return 4, true
	case atom.H5:
		return 5, true
	case atom.H6:
		return 6, true
	}
	return 0, false
}

// textOf 递归收集节点下的全部文本。
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
			return
		}
		if node.Type == html.ElementNode && noiseElements[node.DataAtom] {
			return
		}
		if node.Type == html.ElementNode && blockElements[node.DataAtom] && b.Len() > 0 {
			b.WriteByte(' ')
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return collapseSpaces(b.String())
}

func collapseSpaces(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

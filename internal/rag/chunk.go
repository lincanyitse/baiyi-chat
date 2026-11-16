package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// Chunker 把解析后的 Section 序列切分成检索单元。
//
// 策略（架构纲要 §7.2）：
//   - 代码与表格是原子块，超长时按行硬切并重复围栏/表头
//   - 普通段落打包到目标 token 数，只在段落边界切开
//   - 相邻 chunk 之间按字符重叠，避免答案刚好跨在切口上
type Chunker struct {
	// TargetTokens 是单个 chunk 的目标大小。
	TargetTokens int
	// MaxTokens 是硬上限：原子块超过它才切。
	MaxTokens int
	// Overlap 是重叠比例 [0,1)。
	Overlap float64
	// MinTokens 是合并下限：小于它的尾块并入前一块。
	MinTokens int
}

// NewChunker 用配置值构造切分器。
func NewChunker(targetTokens int, overlap float64) *Chunker {
	if targetTokens <= 0 {
		targetTokens = 600
	}
	if overlap < 0 || overlap >= 1 {
		overlap = 0.12
	}
	return &Chunker{
		TargetTokens: targetTokens,
		MaxTokens:    targetTokens * 2,
		Overlap:      overlap,
		MinTokens:    targetTokens / 5,
	}
}

// Chunk 切分文档并计算字符偏移。
//
// 关键：**跨 Section 打包**。Markdown 解析器把每个段落产成一个 Section，
// 若逐个 Section 独立切分，每个段落都会单独成块——既浪费切分预算，
// 也让重叠机制完全失效。因此这里先按 heading_path 把连续段落归为一组，
// 组内再打包；代码与表格这类原子块各自独立成组。
func (c *Chunker) Chunk(documentID string, doc Doc) []Chunk {
	var out []Chunk

	for _, g := range groupSections(doc.Sections) {
		var pieces []piece
		if g.atomic {
			pieces = c.splitSection(g.sections[0])
		} else {
			pieces = c.splitGroup(g)
		}

		for _, p := range pieces {
			if strings.TrimSpace(p.text) == "" {
				continue
			}
			ch := Chunk{
				DocumentID:  documentID,
				Ordinal:     len(out),
				HeadingPath: g.path,
				Kind:        g.kind,
				Text:        p.text,
				CharStart:   p.start,
				CharEnd:     p.start + len([]rune(p.text)),
				Page:        g.page,
				TokenEst:    llm.EstimateText(p.text),
			}
			ch.ID = chunkID(documentID, ch.Ordinal, ch.Text)
			out = append(out, ch)
		}
	}
	return out
}

// sectionGroup 是同一 heading_path 下连续的可切分段。
type sectionGroup struct {
	path     string
	kind     SectionKind
	page     int
	atomic   bool
	offset   int // 组首 Section 在文档解析文本中的字符偏移
	sections []Section
}

// groupSections 把 Section 序列归组，并记录每组在文档中的起始偏移。
//
// 标题不入组（也不产生 chunk），但它占的字符必须计入偏移——
// 否则引用定位会整体前移，跳转到错误的段落。
func groupSections(secs []Section) []sectionGroup {
	var out []sectionGroup
	cursor := 0

	appendGroup := func(g sectionGroup) {
		g.offset = cursor
		out = append(out, g)
	}

	for _, s := range secs {
		if s.Kind == KindHeading {
			cursor += len([]rune(s.Text))
			continue
		}

		if isAtomic(s.Kind) {
			appendGroup(sectionGroup{
				path: s.HeadingPath, kind: s.Kind, page: s.Page,
				atomic: true, sections: []Section{s},
			})
			cursor += len([]rune(s.Text))
			continue
		}

		last := len(out) - 1
		if last >= 0 && !out[last].atomic && out[last].path == s.HeadingPath && out[last].page == s.Page {
			out[last].sections = append(out[last].sections, s)
			cursor += len([]rune(s.Text))
			continue
		}
		appendGroup(sectionGroup{
			path: s.HeadingPath, kind: s.Kind, page: s.Page, sections: []Section{s},
		})
		cursor += len([]rune(s.Text))
	}
	return out
}

// splitSection 处理单个原子 Section，offset 是该 Section 在文档中的起始字符位置。
func (c *Chunker) splitSection(sec Section) []piece {
	est := llm.EstimateText(sec.Text)
	if est <= c.MaxTokens {
		return []piece{{text: sec.Text, start: sec.CharOffset}}
	}
	pieces := c.splitAtomic(sec, []rune(sec.Text))
	for i := range pieces {
		pieces[i].start += sec.CharOffset
	}
	return pieces
}

// splitGroup 把同组的多段文本打包。
//
// 每段的起始偏移直接取解析器给出的 CharOffset——它是相对整篇文档的
// 精确字符位置，引用回跳就靠它。不在这里推算：中间隔着标题行与空行，
// 推算出来的偏移必然偏小，用户点击引用会跳到错误的段落。
func (c *Chunker) splitGroup(g sectionGroup) []piece {
	type para struct {
		text  string
		start int
		tok   int
	}

	paras := make([]para, len(g.sections))
	for i, s := range g.sections {
		paras[i] = para{
			text:  s.Text,
			start: s.CharOffset,
			tok:   llm.EstimateText(s.Text),
		}
	}
	if len(paras) == 0 {
		return nil
	}

	overlapBudget := int(float64(c.TargetTokens) * c.Overlap)

	var out []piece
	var buf []string
	used := 0
	blockStart := 0

	flush := func() {
		if len(buf) == 0 {
			return
		}
		out = append(out, piece{text: strings.Join(buf, "\n\n"), start: blockStart})
		buf = buf[:0]
		used = 0
	}

	for i, p := range paras {
		if used+p.tok > c.TargetTokens && len(buf) > 0 {
			flush()
			blockStart = p.start
			if overlapBudget > 0 && i > 0 && paras[i-1].tok <= overlapBudget {
				buf = append(buf, paras[i-1].text)
				used += paras[i-1].tok
				blockStart = paras[i-1].start
			}
		} else if len(buf) == 0 {
			blockStart = p.start
		}
		buf = append(buf, p.text)
		used += p.tok
	}
	flush()

	return out
}

// piece 是切分结果的一段，start 是相对文档解析文本的字符偏移。
type piece struct {
	text  string
	start int
}

func isAtomic(k SectionKind) bool {
	return k == KindCode || k == KindTable
}

// splitAtomic 按行切超长原子块，并重复围栏或表头。
func (c *Chunker) splitAtomic(sec Section, runes []rune) []piece {
	lines := strings.Split(sec.Text, "\n")

	var prefix, suffix string
	body := lines
	if sec.Kind == KindCode && len(lines) > 1 {
		if fence, ok := fenceStart(lines[0]); ok {
			prefix = lines[0] + "\n"
			suffix = "\n" + fence
			body = lines[1:]
			// 去掉原结尾围栏
			if len(body) > 0 && isFenceEnd(body[len(body)-1], fence) {
				body = body[:len(body)-1]
			}
		}
	}
	if sec.Kind == KindTable && len(lines) > 2 {
		// 第一行表头 + 第二行分隔线在每段重复
		prefix = lines[0] + "\n" + lines[1] + "\n"
		body = lines[2:]
	}

	var out []piece
	var buf []string
	used := 0
	// offset 累计已消费的字符数，用于精确的 char_start
	offset := 0
	emit := func() {
		if len(buf) == 0 {
			return
		}
		text := prefix + strings.Join(buf, "\n") + suffix
		out = append(out, piece{text: text, start: offset})
		offset += len([]rune(text))
		buf = buf[:0]
		used = 0
	}

	for _, line := range body {
		t := llm.EstimateText(line) + 1
		if used+t > c.TargetTokens && len(buf) > 0 {
			emit()
		}
		buf = append(buf, line)
		used += t
	}
	emit()

	if len(out) == 0 && len(runes) > 0 {
		out = append(out, piece{text: sec.Text, start: 0})
	}
	return out
}

// indexRunes 从 from 开始在 hay 中查找 needle 的字符下标。
func indexRunes(hay, needle []rune, from int) int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return -1
	}
	if from < 0 {
		from = 0
	}
	for i := from; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// chunkID 由文档 ID + 序号 + 内容生成，保证重建后 ID 稳定。
//
// 内容参与哈希：文本变了 ID 就变，旧的 embeddings 行自然失效，
// 不会出现「向量还是旧文本的」这类静默错误。
func chunkID(documentID string, ordinal int, text string) string {
	h := sha256.New()
	h.Write([]byte(documentID))
	h.Write([]byte{0})
	h.Write([]byte(string(rune(ordinal))))
	h.Write([]byte{0})
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))[:24]
}

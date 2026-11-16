// Package textutil 提供中文文本处理的基础谓词与切分。
//
// 为什么独立成包：rag（中文二元组检索）与 memory（关键词抽取、线索匹配）
// 都需要「什么算 CJK」「什么算英文词」「怎么切二元组」，而两者按分层
// 约束不能互相 import。这些函数只依赖标准库与 unicode，放最底层。
package textutil

import (
	"strings"
	"unicode"
)

// IsCJK 判断是否中日韩表意或假名/谚文字符。
func IsCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// IsWordRune 判断是否英文/数字/词内符号（下划线、连字符、点）。
//
// 允许点与连字符是为了让 `10.0.0.1`、`bge-m3` 这类标识符
// 在切分后保持完整。
func IsWordRune(r rune) bool {
	if r > unicode.MaxASCII {
		return false
	}
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
}

// Bigram 把文本切成「中文二元组 + 英文整词」的检索词序列。
//
// 中文没有词边界，按空格切等于不切；而英文整词保留是必须的——
// 否则 `RAG` 会变成 `RA AG`，搜 `RA` 也能命中。
func Bigram(text string) string {
	var out strings.Builder
	var word strings.Builder

	flushWord := func() {
		if word.Len() == 0 {
			return
		}
		if out.Len() > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(strings.ToLower(word.String()))
		word.Reset()
	}

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case IsWordRune(r):
			word.WriteRune(r)
		case IsCJK(r):
			flushWord()
			if i+1 < len(runes) && IsCJK(runes[i+1]) {
				writeTerm(&out, string(runes[i:i+2]))
			}
			// 孤立的单个汉字（前后都不是 CJK）也保留，
			// 否则「一」「的」这类单字查询永远搜不到
			if (i == 0 || !IsCJK(runes[i-1])) && (i+1 >= len(runes) || !IsCJK(runes[i+1])) {
				writeTerm(&out, string(r))
			}
		default:
			flushWord()
		}
	}
	flushWord()
	return out.String()
}

func writeTerm(b *strings.Builder, term string) {
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	b.WriteString(term)
}

// BigramSet 返回文本的字符二元组集合，用于相似度计算。
func BigramSet(s string) map[string]struct{} {
	r := []rune(s)
	out := make(map[string]struct{}, len(r))
	if len(r) == 1 {
		out[string(r)] = struct{}{}
		return out
	}
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])] = struct{}{}
	}
	return out
}

// Jaccard 计算两个集合的 Jaccard 系数。
func Jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// TruncateRunes 按字符（不是字节）截断，超长时追加省略号。
//
// 按字节截断会切坏多字节字符，产出的乱码再被嵌入就会污染索引。
// 用于给人看的展示文本。要截断**送入模型或嵌入的文本**时用
// HeadRunes——省略号会成为内容的一部分。
func TruncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// HeadRunes 按字符截断且不加任何后缀。用于嵌入输入这类
// 「截断内容本身就是要处理的对象」的场景。
func HeadRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// EstimateTokens 粗略估算 token 数：中文约 1.5 字/token。
//
// 只用于预算与配额这类「差一两成不影响正确性」的判断。
// 需要精确计数时用 Provider 的 CountTokens。
func EstimateTokens(s string) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	return n*2/3 + 1
}

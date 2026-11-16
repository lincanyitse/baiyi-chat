package rag

import (
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/infra/textutil"
)

// Bigram 把文本转成 FTS5 的检索词序列。
//
// 中文切二元组、ASCII 与数字按整词保留。实现见 textutil——
// memory 包也要用同一套切分，两边不能各写一份。
func Bigram(text string) string { return textutil.Bigram(text) }

// QueryBigram 把用户查询转成 FTS5 MATCH 表达式，返回两个表达式：
// 严格的（全部二元组 AND）与宽松的（OR）。
//
// 为什么需要两档：用户输入「白泽助理」，切出的二元组里
// 「泽助」是两个词的跨界产物，原文里根本没有——严格匹配会零命中。
// 先用严格式保住精度，零命中时再退到宽松式保住召回。
func QueryBigram(query string) (strict, loose string) {
	terms := strings.Fields(Bigram(query))
	if len(terms) == 0 {
		return "", ""
	}
	const maxTerms = 16
	if len(terms) > maxTerms {
		terms = terms[:maxTerms]
	}

	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " "), strings.Join(quoted, " OR ")
}

// QueryTerms 返回用于降级的词项列表（空格分隔），
// 供 FTS5 不可用时的简单 LIKE 匹配使用。
func QueryTerms(query string) []string {
	return strings.Fields(Bigram(query))
}

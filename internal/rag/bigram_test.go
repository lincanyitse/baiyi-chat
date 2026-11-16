package rag

import (
	"strings"
	"testing"
)

func TestBigramChinese(t *testing.T) {
	got := Bigram("白泽助手")
	want := "白泽 泽助 助手"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBigramKeepsASCIIWholeWord(t *testing.T) {
	got := Bigram("使用 RAG 检索")
	// ASCII 整词保留，不能拆成 RA AG
	if strings.Contains(got, "ra ag") {
		t.Fatalf("ASCII 词被拆开了：%q", got)
	}
	if !strings.Contains(got, "rag") {
		t.Fatalf("应保留小写的 rag 整词：%q", got)
	}
}

func TestBigramLowercasesASCII(t *testing.T) {
	a := Bigram("PostgreSQL")
	b := Bigram("postgresql")
	if a != b {
		t.Fatalf("大小写应归一：%q vs %q", a, b)
	}
}

func TestBigramSingleCJKKept(t *testing.T) {
	// 前后都不是 CJK 的单个汉字要保留，否则单字查询永远搜不到
	got := Bigram("a我b")
	if !strings.Contains(got, "我") {
		t.Fatalf("孤立汉字应保留：%q", got)
	}
}

func TestBigramMixed(t *testing.T) {
	got := Bigram("白泽跑在 Windows 上")
	if !strings.Contains(got, "白泽") {
		t.Fatalf("中文二元组缺失：%q", got)
	}
	if !strings.Contains(got, "windows") {
		t.Fatalf("英文整词缺失：%q", got)
	}
}

func TestBigramEmpty(t *testing.T) {
	if got := Bigram(""); got != "" {
		t.Fatalf("空串应返回空：%q", got)
	}
	if got := Bigram("。，！"); got != "" {
		t.Fatalf("纯标点应返回空：%q", got)
	}
}

func TestQueryBigramTwoForms(t *testing.T) {
	strict, loose := QueryBigram("白泽 部署")
	if strict == "" || loose == "" {
		t.Fatal("应返回两个表达式")
	}
	if strict == loose {
		t.Fatal("单个词时两式相同，此处应不同")
	}
	if !strings.Contains(strict, `"白泽"`) {
		t.Fatalf("严格式应含带引号的二元组：%q", strict)
	}
	if !strings.Contains(loose, " OR ") {
		t.Fatalf("宽松式应用 OR 连接：%q", loose)
	}
}

func TestQueryBigramEscapesQuotes(t *testing.T) {
	strict, _ := QueryBigram(`说"话"`)
	if strings.Count(strict, `"`)%2 != 0 {
		t.Fatalf("引号未转义导致 phrase 不闭合：%q", strict)
	}
}

func TestQueryBigramEmpty(t *testing.T) {
	strict, loose := QueryBigram("！！")
	if strict != "" || loose != "" {
		t.Fatalf("无有效词应返回空：%q %q", strict, loose)
	}
}

func TestQueryTerms(t *testing.T) {
	terms := QueryTerms("白泽部署")
	if len(terms) != 3 {
		t.Fatalf("应切成 3 个二元组，实际 %v", terms)
	}
}

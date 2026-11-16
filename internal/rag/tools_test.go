package rag

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

func TestSearchToolSpec(t *testing.T) {
	spec := NewSearchTool(nil).Spec()
	if spec.Name != "rag_search" {
		t.Fatalf("名称错误：%s", spec.Name)
	}
	if spec.Risk != tool.RiskRead {
		t.Fatalf("检索应为只读，实际 %v", spec.Risk)
	}
	if !json.Valid(spec.InputSchema) {
		t.Fatal("schema 不是合法 JSON")
	}
	if !strings.Contains(spec.Description, "知识库") {
		t.Fatal("描述应说明用途，否则模型不知道何时调用")
	}
}

func TestSearchToolRejectsEmptyQuery(t *testing.T) {
	res, err := NewSearchTool(nil).Execute(context.Background(), json.RawMessage(`{"query":"  "}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("空查询应返回错误结果")
	}
}

func TestSearchToolWithoutRetriever(t *testing.T) {
	res, err := NewSearchTool(nil).Execute(context.Background(), json.RawMessage(`{"query":"甲"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Text(), "未启用") {
		t.Fatalf("未配置时应如实报告：%q", res.Text())
	}
}

func TestSearchToolReturnsCitationsAndTaints(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	ingest(t, r, "a.md", "# 部署\n\n容器部署要拉取镜像。\n")

	var taint tool.TaintState
	ec := &tool.ExecContext{Taint: &taint}

	rt := NewRetriever(r, nil, nil, RetrieverOptions{})
	st := NewSearchTool(rt)
	res, err := st.Execute(ctx, json.RawMessage(`{"query":"容器部署"}`), ec)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("不该报错：%s", res.Text())
	}
	if !strings.Contains(res.Text(), "[1]") {
		t.Fatalf("应带编号引用：%q", res.Text())
	}

	// 结果里应含 CitationBlock，供前端跳转
	var cites int
	for _, b := range res.Blocks {
		if strings.Contains(string(mustJSON(b)), "citation") {
			cites++
		}
	}
	if cites == 0 {
		t.Fatal("应附 CitationBlock")
	}

	// RAG 结果属于外部内容，必须置污点
	if !taint.IsTainted() {
		t.Fatal("检索结果应标记污点，否则后续写操作会绕过审批")
	}
}

func TestSearchToolLimitClamp(t *testing.T) {
	in, err := ParseSearchInput(json.RawMessage(`{"query":"甲","limit":999}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Limit != 20 {
		t.Fatalf("limit 应被夹到 20，实际 %d", in.Limit)
	}

	in, err = ParseSearchInput(json.RawMessage(`{"query":"甲","limit":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Limit != 8 {
		t.Fatalf("默认应为 8，实际 %d", in.Limit)
	}
}

func TestDocumentListTool(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	lt := NewDocumentListTool(r)
	res, err := lt.Execute(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text(), "空") {
		t.Fatalf("空库应如实报告：%q", res.Text())
	}

	ingest(t, r, "a.md", "# 甲\n\n正文。\n")
	res, err = lt.Execute(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text(), "a") {
		t.Fatalf("应列出文档：%q", res.Text())
	}
	if !strings.Contains(res.Text(), "就绪") {
		t.Fatalf("应显示状态：%q", res.Text())
	}
}

func TestCitationBlockEmptyResult(t *testing.T) {
	if got := CitationBlock(SearchResult{}); !strings.Contains(got, "没有找到") {
		t.Fatalf("空结果应明确说明：%q", got)
	}
	got := CitationBlock(SearchResult{Degraded: true, Reason: "向量不可用"})
	if !strings.Contains(got, "向量不可用") {
		t.Fatalf("降级原因应可见：%q", got)
	}
}

func TestCitationBlocksCarryLocators(t *testing.T) {
	res := SearchResult{Chunks: []Citation{{
		ChunkID: "c1", DocumentID: "d1", Title: "手册",
		HeadingPath: "甲 / 乙", Page: 3, CharStart: 100, CharEnd: 150,
		Snippet: "正文内容",
	}}}
	blocks := CitationBlocks(res)
	if len(blocks) != 1 {
		t.Fatalf("应产出 1 个引用块，实际 %d", len(blocks))
	}
	raw := string(mustJSON(blocks[0]))
	for _, want := range []string{"c1", "d1", "甲 / 乙", "正文内容"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("引用块缺少 %q：%s", want, raw)
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

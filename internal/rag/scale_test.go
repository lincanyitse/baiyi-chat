package rag

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/embed"
)

// buildCorpus 生成 n 页规模的 Markdown：每页一个一级标题、若干二级标题与段落。
func buildCorpus(pages int) string {
	var b strings.Builder
	for p := range pages {
		fmt.Fprintf(&b, "# 第 %d 章\n\n", p+1)
		for s := range 4 {
			fmt.Fprintf(&b, "## 第 %d 节\n\n", s+1)
			for para := range 3 {
				fmt.Fprintf(&b, "第 %d 章第 %d 节的第 %d 段内容。"+
					"这里描述一个具体的技术要点，编号 %d-%d-%d，"+
					"用于验证切分、检索与引用的整体正确性。\n\n",
					p+1, s+1, para+1, p+1, s+1, para+1)
			}
		}
	}
	return b.String()
}

// TestCorpusScaleAndRetrieval 是「200 页语料」验收的自动化等价物。
func TestCorpusScaleAndRetrieval(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过规模测试")
	}

	r, _ := newTestRepo(t)
	ctx := context.Background()

	const pages = 200
	content := buildCorpus(pages)

	start := time.Now()
	doc := ingest(t, r, "corpus.md", content)
	ingestDur := time.Since(start)

	chunks, err := r.ChunksByDocument(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d 页 → %d 字 → %d chunks，接入耗时 %v",
		pages, doc.CharCount, len(chunks), ingestDur.Round(time.Millisecond))

	// 切分应产生合理数量：每页约 4-8 块
	if len(chunks) < pages*2 {
		t.Fatalf("chunk 数偏少：%d（%d 页）", len(chunks), pages)
	}
	if len(chunks) > pages*20 {
		t.Fatalf("chunk 数异常膨胀：%d（%d 页）", len(chunks), pages)
	}

	// 每块都应带标题路径、合法偏移、合理大小
	for i, c := range chunks {
		if c.HeadingPath == "" {
			t.Fatalf("第 %d 块缺少 heading_path", i)
		}
		if c.CharStart < 0 || c.CharEnd <= c.CharStart {
			t.Fatalf("第 %d 块偏移非法：[%d,%d)", i, c.CharStart, c.CharEnd)
		}
		if c.TokenEst > 1600 {
			t.Fatalf("第 %d 块 %d token 超过硬上限", i, c.TokenEst)
		}
	}

	// 重复接入同内容：不新增文档、不重复 chunk
	before := len(chunks)
	again := ingest(t, r, "corpus-copy.md", content)
	if again.ID != doc.ID {
		t.Fatal("同内容应复用同一文档")
	}
	after, _ := r.ChunksByDocument(ctx, doc.ID)
	if len(after) != before {
		t.Fatalf("重复接入改变了 chunk 数：%d → %d", before, len(after))
	}

	// 关键词检索应能定位到对应章节
	start = time.Now()
	hits, err := r.SearchKeyword(ctx, "第 137 章第 3 节", 10)
	searchDur := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("应能检索到第 137 章")
	}
	top, err := r.ChunkByID(ctx, hits[0].ChunkID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(top.HeadingPath, "第 137 章") {
		t.Fatalf("top-1 章节错误：%s", top.HeadingPath)
	}
	t.Logf("关键词检索 top-1 正确，耗时 %v", searchDur.Round(time.Microsecond))
}

// TestCorpusVectorRetrieval 用 fake 嵌入验证向量路径在规模下仍然可用。
func TestCorpusVectorRetrieval(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过规模测试")
	}

	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "corpus.md", buildCorpus(60))
	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "fake"})
	f := embed.NewFake(256)
	ing.SetEmbedder(f)

	if err := ing.EmbedDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}

	n, err := r.CountVectors(ctx, "chunk", "fake")
	if err != nil {
		t.Fatal(err)
	}
	chunks, _ := r.ChunksByDocument(ctx, doc.ID)
	if n != len(chunks) {
		t.Fatalf("向量数 %d 与片段数 %d 不符", n, len(chunks))
	}

	ret := NewRetriever(r, f, nil, RetrieverOptions{Model: "fake", TopKVector: 20, TopKRerank: 5})
	res, err := ret.Search(ctx, "第 42 章的技术要点", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) == 0 {
		t.Fatal("应有结果")
	}
	if res.Degraded {
		t.Fatal("配置了嵌入不该降级")
	}
}

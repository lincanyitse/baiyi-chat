package rag

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/embed"
)

// dimEmbedder 返回指定维度的确定性向量，用来模拟「换了一个嵌入模型」。
type dimEmbedder struct {
	name  string
	dims  int
	calls int
}

func (e *dimEmbedder) Name() string          { return e.name }
func (e *dimEmbedder) Dimensions(string) int { return e.dims }
func (e *dimEmbedder) Embed(ctx context.Context, model string, texts []string) ([][]float32, embed.Usage, error) {
	e.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, e.dims)
		for j := range v {
			v[j] = float32(len(t)%7+j%5) + 1
		}
		embed.Normalize(v)
		out[i] = v
	}
	return out, embed.Usage{InputTokens: len(texts) * 10, Requests: 1}, nil
}

// TestModelSwitchKeepsOldVectors 验证换模型后旧向量仍然可复用。
//
// 这是「是否保留旧向量」这个问题的实测答案：**保留**。
// 主键是 (owner_kind, owner_id, model)，两个模型的向量天然共存；
// 切回旧模型时 0 次嵌入调用即可恢复检索能力，不必重算。
func TestModelSwitchKeepsOldVectors(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "a.md", "# 甲\n\n这是一段用来测试模型切换的正文内容。\n")

	oldModel := &dimEmbedder{name: "old", dims: 64}
	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "old"})
	ing.SetEmbedder(oldModel)

	// 用旧模型建索引
	if err := ing.EmbedDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}
	oldCalls := oldModel.calls
	oldN, _ := r.CountVectors(ctx, "chunk", "old")
	if oldN == 0 {
		t.Fatal("旧模型应有向量")
	}

	// 换新模型重建
	newModel := &dimEmbedder{name: "new", dims: 128}
	ing2 := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "new"})
	ing2.SetEmbedder(newModel)
	if _, err := ing2.RebuildEmbeddings(ctx, "new"); err != nil {
		t.Fatal(err)
	}

	// 两个模型的向量并存
	nOld, _ := r.CountVectors(ctx, "chunk", "old")
	nNew, _ := r.CountVectors(ctx, "chunk", "new")
	if nOld != oldN {
		t.Fatalf("换模型不该删除旧向量：%d → %d", oldN, nOld)
	}
	if nNew == 0 {
		t.Fatal("新模型应有向量")
	}

	// 切回旧模型：不需要任何嵌入调用
	before := oldModel.calls
	ing3 := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "old"})
	ing3.SetEmbedder(oldModel)
	rc, err := ing3.RebuildEmbeddings(ctx, "old")
	if err != nil {
		t.Fatal(err)
	}
	if rc.Done != 0 {
		t.Fatalf("切回旧模型不该重算，实际处理 %d 条", rc.Done)
	}
	if oldModel.calls != before {
		t.Fatalf("切回旧模型不该调用嵌入服务：%d → %d", before, oldModel.calls)
	}
	t.Logf("旧模型 %s（%d 维）向量保留，切回零重算", oldModel.name, oldModel.dims)

	// 也验证新旧模型各自可检索，互不干扰
	_ = oldCalls
	for _, m := range []struct {
		model    string
		embedder embed.Embedder
	}{{"old", oldModel}, {"new", newModel}} {
		ret := NewRetriever(r, m.embedder, nil, RetrieverOptions{Model: m.model})
		res, err := ret.Search(ctx, "模型切换", 3)
		if err != nil {
			t.Fatalf("%s 模型检索失败: %v", m.model, err)
		}
		if len(res.Chunks) == 0 {
			t.Fatalf("%s 模型应能检索到结果", m.model)
		}
	}
}

// TestModelSwitchDimensionIsolation 验证不同维度不会串味。
func TestModelSwitchDimensionIsolation(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "a.md", "# 乙\n\n维度隔离测试正文，需要足够长以产生至少一个片段。\n")

	a := &dimEmbedder{name: "a", dims: 64}
	b := &dimEmbedder{name: "b", dims: 1024}

	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "a"})
	ing.SetEmbedder(a)
	if err := ing.EmbedDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}

	ing2 := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "b"})
	ing2.SetEmbedder(b)
	if _, err := ing2.RebuildEmbeddings(ctx, "b"); err != nil {
		t.Fatal(err)
	}

	rowsA, _ := r.LoadVectors(ctx, "chunk", "a")
	rowsB, _ := r.LoadVectors(ctx, "chunk", "b")
	if len(rowsA) == 0 || len(rowsB) == 0 {
		t.Fatal("两个模型都应有向量")
	}
	if len(rowsA[0].Vec) != 64 {
		t.Fatalf("模型 a 的向量维度应为 64，实际 %d", len(rowsA[0].Vec))
	}
	if len(rowsB[0].Vec) != 1024 {
		t.Fatalf("模型 b 的向量维度应为 1024，实际 %d", len(rowsB[0].Vec))
	}
}

// TestSwitchBackFasterThanRecompute 用调用次数证明「切回旧模型免重算」。
func TestSwitchBackFasterThanRecompute(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	var b strings.Builder
	b.WriteString("# 大规模\n\n")
	for i := range 40 {
		fmt.Fprintf(&b, "第 %d 段正文，内容足够长以独立成块，用于观察嵌入调用次数。\n\n", i)
	}
	doc := ingest(t, r, "big.md", b.String())

	m := &dimEmbedder{name: "m", dims: 96}
	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "m"})
	ing.SetEmbedder(m)

	if err := ing.EmbedDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}
	firstCalls := m.calls

	if _, err := ing.RebuildEmbeddings(ctx, "m"); err != nil {
		t.Fatal(err)
	}
	if m.calls != firstCalls {
		t.Fatalf("已建索引再重建不该再调用：%d → %d", firstCalls, m.calls)
	}
}

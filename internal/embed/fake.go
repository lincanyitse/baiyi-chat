package embed

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
)

// Fake 是确定性伪嵌入后端，用于测试与「没配嵌入服务」的开发环境。
//
// 它把文本哈希成一个固定维度的单位向量：同样的文本永远得到同样的向量，
// 相同前缀的文本向量相近（因为按哈希桶累加），所以余弦检索能返回
// 「看起来合理」的结果——但**没有语义**，不能用于评估检索质量。
//
// 日志里会带 fake 标记，避免误以为在跑真模型。
type Fake struct {
	dims int
	// Tainted 为真时模拟服务不可用，用于测试降级路径。
	Unavailable bool
}

// NewFake 构造伪嵌入后端。dims 为 0 时默认 1024（对齐 bge-m3）。
func NewFake(dims int) *Fake {
	if dims <= 0 {
		dims = 1024
	}
	return &Fake{dims: dims}
}

// Name 实现 Embedder。
func (f *Fake) Name() string { return "fake" }

// Dimensions 实现 Embedder。
func (f *Fake) Dimensions(string) int { return f.dims }

// Embed 实现 Embedder。
func (f *Fake) Embed(ctx context.Context, model string, texts []string) ([][]float32, Usage, error) {
	if f.Unavailable {
		return nil, Usage{}, fmt.Errorf("%w: fake 后端被置为不可用", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, Usage{}, err
	}

	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = f.vectorize(text)
	}
	return out, Usage{
		InputTokens: estimateTokens(texts),
		Requests:    len(texts),
		Model:       "fake",
	}, nil
}

// vectorize 把文本映射为确定性单位向量。
//
// 用滑动的 3 字符窗口逐个入桶：内容重叠的文本共享多数窗口，向量因此相近。
// 这足以让「同一份资料切出的相邻 chunk」互相检索到。
func (f *Fake) vectorize(text string) []float32 {
	v := make([]float32, f.dims)
	runes := []rune(text)
	if len(runes) == 0 {
		v[0] = 1
		return v
	}

	window := min(3, len(runes))
	for i := 0; i+window <= len(runes); i++ {
		h := fnv.New32a()
		h.Write([]byte(string(runes[i : i+window])))
		sum := h.Sum32()
		bucket := int(sum % uint32(f.dims))
		// 用另一个比特决定符号，避免全正导致向量高度相似
		if sum&(1<<31) != 0 {
			v[bucket] -= 1
		} else {
			v[bucket] += 1
		}
	}
	Normalize(v)

	// 极小文本可能量化后全是 0，兜底给一个非零向量
	var sum float64
	for _, x := range v {
		sum += math.Abs(float64(x))
	}
	if sum == 0 {
		v[0] = 1
	}
	return v
}

// FakeReranker 是确定性的伪重排器：按查询与文档共有的字符二元组比例打分。
//
// 同样只有字面相关性，没有语义理解。用于测试重排链路而不产生费用。
type FakeReranker struct {
	Unavailable bool
}

// Name 实现 Reranker。
func (f *FakeReranker) Name() string { return "fake" }

// Rerank 实现 Reranker。
func (f *FakeReranker) Rerank(ctx context.Context, model, query string, docs []string) ([]float32, Usage, error) {
	if f.Unavailable {
		return nil, Usage{}, fmt.Errorf("%w: fake 重排器被置为不可用", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, Usage{}, err
	}
	scores := make([]float32, len(docs))
	q := bigrams(query)
	for i, d := range docs {
		db := bigrams(d)
		if len(q) == 0 || len(db) == 0 {
			continue
		}
		common := 0
		for g := range q {
			if db[g] {
				common++
			}
		}
		scores[i] = float32(common) / float32(len(q))
	}
	total := 0
	for _, d := range docs {
		total += len([]rune(d))
	}
	return scores, Usage{InputTokens: total * 2 / 3, Requests: 1, Model: "fake"}, nil
}

func bigrams(s string) map[string]bool {
	r := []rune(s)
	out := make(map[string]bool, len(r))
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])] = true
	}
	return out
}

func estimateTokens(texts []string) int {
	total := 0
	for _, t := range texts {
		total += len([]rune(t))*2/3 + 1
	}
	return total
}

var _ Embedder = (*Fake)(nil)
var _ Reranker = (*FakeReranker)(nil)

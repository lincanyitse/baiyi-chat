package embed

import (
	"math"
)

// Normalize 把向量归一化为单位长度，使余弦相似度退化为点积。
//
// 写入时归一化一次，检索时只需点积——省掉每次查询开方。
// 零向量原样返回（调用方应视为无效向量）。
func Normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}

// QuantizeInt8 把归一化后的 f32 向量量化到 int8。
//
// 归一化后分量落在 [-1, 1]，乘以 127 映射到 [-127, 127]。
// 单向量内存从 4 bytes/维降到 1 byte/维：10 万 × 1024 维从 410MB 降到 102MB。
func QuantizeInt8(v []float32) []int8 {
	out := make([]int8, len(v))
	for i, x := range v {
		if x > 1 {
			x = 1
		} else if x < -1 {
			x = -1
		}
		out[i] = int8(math.Round(float64(x) * 127))
	}
	return out
}

// Dequantize 把 int8 还原为 f32（用于精排）。
func Dequantize(q []int8) []float32 {
	out := make([]float32, len(q))
	for i, x := range q {
		out[i] = float32(x) / 127
	}
	return out
}

// DotInt8 计算两个量化向量的点积。
//
// 用 int32 累加避免溢出：127 × 127 × 1024 ≈ 1.65e7，远小于 int32 上限。
func DotInt8(a, b []int8) float32 {
	n := min(len(a), len(b))
	var sum int32
	for i := range n {
		sum += int32(a[i]) * int32(b[i])
	}
	// 除以 127² 还原到 [-1, 1] 量级
	return float32(sum) / (127 * 127)
}

// Dot 计算两个 f32 向量的点积（调用方保证已归一化）。
func Dot(a, b []float32) float32 {
	n := min(len(a), len(b))
	var sum float32
	for i := range n {
		sum += a[i] * b[i]
	}
	return sum
}

// Hit 是一条检索结果。
type Hit struct {
	ID    string
	Score float32
}

// TopK 从量化向量集合中取点积最高的 k 条。
//
// 用容量 k 的最小堆避免全排序：10 万条向量只需 O(n log k)。
// ids 与 vecs 必须等长且顺序对应。
func TopK(query []int8, ids []string, vecs [][]int8, k int) []Hit {
	if k <= 0 || len(ids) == 0 {
		return nil
	}
	if k > len(ids) {
		k = len(ids)
	}

	h := newMinHeap(k)
	for i, vec := range vecs {
		score := DotInt8(query, vec)
		h.offer(Hit{ID: ids[i], Score: score})
	}
	return h.sortedDesc()
}

// minHeap 是容量受限的最小堆：堆顶是当前 top-k 里最小的。
type minHeap struct {
	items []Hit
	cap   int
}

func newMinHeap(capacity int) *minHeap {
	return &minHeap{items: make([]Hit, 0, capacity+1), cap: capacity}
}

func (h *minHeap) offer(x Hit) {
	if len(h.items) < h.cap {
		h.items = append(h.items, x)
		h.up(len(h.items) - 1)
		return
	}
	if x.Score <= h.items[0].Score {
		return
	}
	h.items[0] = x
	h.down(0)
}

func (h *minHeap) up(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if h.items[parent].Score <= h.items[i].Score {
			break
		}
		h.items[parent], h.items[i] = h.items[i], h.items[parent]
		i = parent
	}
}

func (h *minHeap) down(i int) {
	n := len(h.items)
	for {
		left, right := 2*i+1, 2*i+2
		smallest := i
		if left < n && h.items[left].Score < h.items[smallest].Score {
			smallest = left
		}
		if right < n && h.items[right].Score < h.items[smallest].Score {
			smallest = right
		}
		if smallest == i {
			return
		}
		h.items[i], h.items[smallest] = h.items[smallest], h.items[i]
		i = smallest
	}
}

// sortedDesc 返回按分数降序排列的全部结果。
func (h *minHeap) sortedDesc() []Hit {
	out := make([]Hit, len(h.items))
	copy(out, h.items)
	// 堆排序：反复取堆顶（最小值）放到末尾
	for i := len(out) - 1; i > 0; i-- {
		out[0], out[i] = out[i], out[0]
		// 对前 i 个元素重新下沉
		j := 0
		for {
			left, right := 2*j+1, 2*j+2
			smallest := j
			if left < i && out[left].Score < out[smallest].Score {
				smallest = left
			}
			if right < i && out[right].Score < out[smallest].Score {
				smallest = right
			}
			if smallest == j {
				break
			}
			out[j], out[smallest] = out[smallest], out[j]
			j = smallest
		}
	}
	return out
}

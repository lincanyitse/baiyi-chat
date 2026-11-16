package rag

import "sort"

// rrfK 是 RRF 的平滑常数。
//
// k=60 是论文推荐值，作用是把「排名第一」与「排名第十」的分数差距压平：
// 两路召回的质量本来就不对等，分数线不可比，只有名次可比较。
const rrfK = 60

// RRF 融合多路召回的排名。
//
//	score(d) = Σ 1 / (k + rank_i(d))
//
// rank 从 1 开始。某路没召回该文档时该项为 0。
func RRF(lists ...[]string) []Hit {
	type acc struct {
		id    string
		score float64
		best  int // 最好的名次，用于同分时的稳定排序
	}
	m := make(map[string]*acc)

	for _, list := range lists {
		seen := make(map[string]bool, len(list))
		for i, id := range list {
			if id == "" || seen[id] {
				continue // 同一路里重复出现只算一次
			}
			seen[id] = true
			rank := i + 1
			a, ok := m[id]
			if !ok {
				a = &acc{id: id, best: rank}
				m[id] = a
			} else if rank < a.best {
				a.best = rank
			}
			a.score += 1.0 / float64(rrfK+rank)
		}
	}

	out := make([]Hit, 0, len(m))
	for _, a := range m {
		out = append(out, Hit{ID: a.id, Score: float32(a.score), BestRank: a.best})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].BestRank != out[j].BestRank {
			return out[i].BestRank < out[j].BestRank
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Hit 是一条融合后的召回结果。
type Hit struct {
	ID    string
	Score float32
	// BestRank 是它在任意一路里的最好名次。
	BestRank int
}

// topN 截断到前 n 条。
func topN(hits []Hit, n int) []Hit {
	if n <= 0 || len(hits) <= n {
		return hits
	}
	return hits[:n]
}

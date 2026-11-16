package cost

// 按时间范围聚合成本。
//
// 为什么不复用 SessionSummary：它按 turn_id 集合过滤，而管理面板问的
// 是「今天花了多少」——那是时间维度的问题，走 llm_calls.created_at
// 的索引（Unix 秒）比先查 turns 再拼 IN 子句直接得多。

import (
	"context"
	"sort"
	"time"
)

// Report 是一段时期的用量汇总。
type Report struct {
	// From / To 是统计区间（含头不含尾）。
	From time.Time `json:"from"`
	To   time.Time `json:"to"`

	Calls  int     `json:"calls"`
	Input  int     `json:"input_tokens"`
	Output int     `json:"output_tokens"`
	Cached int     `json:"cached_tokens"`
	Cost   float64 `json:"cost"`

	// ByModel 是按模型拆分的明细，按费用降序。
	ByModel []ModelUsage `json:"by_model"`
}

// ModelUsage 是单个模型的用量。
type ModelUsage struct {
	Provider string  `json:"provider"`
	Model    string  `json:"model"`
	Calls    int     `json:"calls"`
	Input    int     `json:"input_tokens"`
	Output   int     `json:"output_tokens"`
	Cached   int     `json:"cached_tokens"`
	Cost     float64 `json:"cost"`
	// Priced 为假表示这个模型没配价格，费用按 0 计但仍统计 token。
	Priced bool `json:"priced"`
}

// Range 解析区间名。返回区间起点；end 恒为 now。
func Range(name string, now time.Time) (time.Time, bool) {
	switch name {
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), true
	case "week":
		y, m, d := now.Date()
		start := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
		// 向前回到周一
		offset := (int(start.Weekday()) + 6) % 7
		return start.AddDate(0, 0, -offset), true
	case "month":
		y, m, _ := now.Date()
		return time.Date(y, m, 1, 0, 0, 0, 0, now.Location()), true
	case "all", "":
		return time.Unix(0, 0), true
	default:
		return time.Time{}, false
	}
}

// Report 汇总指定区间的用量。
//
// 费用按每条调用发生时的价格计算，不是按当前价重算：
// 改了今天的价格不该改写上个月的花费（FR-OPS-002）。
// 价格表按 effective_from 存了历史版本，所以这里取出调用时间
// 再查当时生效的那一档。
func (l *Ledger) Report(ctx context.Context, from, to time.Time) (Report, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT provider, model, input_tokens, output_tokens, cached_tokens, created_at
		 FROM llm_calls
		 WHERE created_at >= ? AND created_at < ?`,
		from.Unix(), to.Unix())
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()

	byModel := make(map[string]*ModelUsage)

	rep := Report{From: from, To: to}
	for rows.Next() {
		var (
			provider, model            string
			in, out, cached, createdAt int64
		)
		if err := rows.Scan(&provider, &model, &in, &out, &cached, &createdAt); err != nil {
			return Report{}, err
		}

		key := provider + "/" + model
		u := byModel[key]
		if u == nil {
			u = &ModelUsage{Provider: provider, Model: model}
			byModel[key] = u
		}

		u.Calls++
		u.Input += int(in)
		u.Output += int(out)
		u.Cached += int(cached)

		if fee, ok := l.ComputeAt(provider, model, int(in), int(out), int(cached),
			time.Unix(createdAt, 0)); ok {
			u.Cost += fee
			u.Priced = true
		}

		rep.Calls++
		rep.Input += int(in)
		rep.Output += int(out)
		rep.Cached += int(cached)
	}
	if err := rows.Err(); err != nil {
		return Report{}, err
	}

	for _, u := range byModel {
		rep.Cost += u.Cost
		rep.ByModel = append(rep.ByModel, *u)
	}

	// 费用高的排前面：用户最关心钱花在哪
	sort.Slice(rep.ByModel, func(i, j int) bool {
		return rep.ByModel[i].Cost > rep.ByModel[j].Cost
	})
	return rep, nil
}

// Package cost 负责模型调用记账与价格查询。
//
// 价格是带生效日期的数据而非代码常量：价格会变，历史记录不应被追溯改写。
package cost

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/infra/idgen"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// DB 是账本需要的持久化能力。
//
// 只暴露执行与查询：账本不迁移 schema、也不关心文件路径。
// *sql.DB 与 *sqlite.DB 都满足。
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Price 是某模型在某个生效时间点之后的价格（单位：元 / 百万 token）。
type Price struct {
	Provider      string
	Model         string
	Currency      string
	In            float64
	Out           float64
	Cached        float64
	EffectiveFrom time.Time
}

// Record 是一次模型调用的记账条目。
type Record struct {
	TurnID          string
	Step            int
	Provider        string
	Model           string
	Purpose         string
	InputTokens     int
	OutputTokens    int
	CachedTokens    int
	ReasoningTokens int
	Latency         time.Duration
	TTFT            time.Duration
	PriceVersion    string
	ErrorKind       llm.ErrorKind
}

// Ledger 记录调用明细并计算费用。
type Ledger struct {
	db     DB
	mu     sync.RWMutex
	prices map[string]Price // key: provider/model
	now    func() time.Time
}

// NewLedger 构造账本并从 prices 表加载当前生效价格。
func NewLedger(db DB) (*Ledger, error) {
	l := &Ledger{db: db, prices: make(map[string]Price), now: time.Now}
	if err := l.loadPrices(context.Background()); err != nil {
		return nil, err
	}
	return l, nil
}

// priceKey 是价格表的内部索引键：provider/model@生效时间。
//
// 带上生效时间是必须的——同一个模型可以有多个历史版本，
// 只按 provider/model 存会让后来的覆盖先前的，「历史成本按记录时价」
// 就退化成了「按当前价重算」。
func priceKey(provider, model string, at time.Time) string {
	return provider + "/" + model + "@" + at.UTC().Format(time.RFC3339)
}

// SetPrice 写入手工配置的价格（M0 无管理界面，价格来自配置）。
//
// 未指定生效时间时按「现在」处理，并截断到秒：记账的 created_at
// 是秒精度，价格若带微秒会让同一秒内的调用查不到这一档。
func (l *Ledger) SetPrice(p Price) {
	if p.Currency == "" {
		p.Currency = "CNY"
	}
	if p.EffectiveFrom.IsZero() {
		p.EffectiveFrom = l.now().Truncate(time.Second)
	}
	l.mu.Lock()
	l.prices[priceKey(p.Provider, p.Model, p.EffectiveFrom)] = p
	l.mu.Unlock()
}

// PriceOf 返回当前生效价格。未配置时返回零值与 false。
func (l *Ledger) PriceOf(provider, model string) (Price, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var best Price
	found := false
	now := l.now()
	prefix := provider + "/" + model + "@"
	for key, p := range l.prices {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if p.EffectiveFrom.After(now) {
			continue
		}
		if !found || p.EffectiveFrom.After(best.EffectiveFrom) {
			best = p
			found = true
		}
	}
	return best, found
}

// PriceAt 返回某模型在指定时刻生效的价格。
//
// 价格是有生效日期的历史数据：改了今天的价格不该改写上个月的花费。
// 表里按 effective_from 存了多个版本，这里取 t 之前最近的那个。
func (l *Ledger) PriceAt(provider, model string, t time.Time) (Price, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var best Price
	found := false
	prefix := provider + "/" + model
	for key, p := range l.prices {
		if !strings.HasPrefix(key, prefix+"@") {
			continue
		}
		if p.EffectiveFrom.After(t) {
			continue
		}
		if !found || p.EffectiveFrom.After(best.EffectiveFrom) {
			best = p
			found = true
		}
	}
	return best, found
}

// ComputeAt 用调用发生时的价格计算费用。
func (l *Ledger) ComputeAt(provider, model string, in, out, cached int, t time.Time) (float64, bool) {
	p, ok := l.PriceAt(provider, model, t)
	if !ok {
		return 0, false
	}
	return fee(p, in, out, cached), true
}

// fee 按价格计算一次调用。
func fee(p Price, in, out, cached int) float64 {
	const perMillion = 1_000_000.0
	uncachedIn := max(in-cached, 0)
	return float64(uncachedIn)/perMillion*p.In +
		float64(cached)/perMillion*p.Cached +
		float64(out)/perMillion*p.Out
}

// Compute 计算一次调用的费用（按当前生效价格）。
//
// 给「还没发生的调用」估算用。历史报表应该用 ComputeAt，
// 否则改价会把过去的花费一起改写。
func (l *Ledger) Compute(provider, model string, in, out, cached int) (float64, bool) {
	p, ok := l.PriceOf(provider, model)
	if !ok {
		return 0, false
	}
	return fee(p, in, out, cached), true
}

// Record 写入一条调用明细。
func (l *Ledger) Record(ctx context.Context, rec Record) error {
	purpose := rec.Purpose
	if purpose == "" {
		purpose = "chat"
	}
	priceVersion := rec.PriceVersion
	if priceVersion == "" {
		if p, ok := l.PriceOf(rec.Provider, rec.Model); ok {
			priceVersion = p.EffectiveFrom.Format("2006-01-02")
		}
	}
	var errKind any
	if rec.ErrorKind != "" {
		errKind = string(rec.ErrorKind)
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO llm_calls(id, turn_id, step, provider, model, purpose,
		    input_tokens, output_tokens, cached_tokens, reasoning_tokens,
		    latency_ms, ttft_ms, price_version, error_kind, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idgen.New(), rec.TurnID, rec.Step, rec.Provider, rec.Model, purpose,
		rec.InputTokens, rec.OutputTokens, rec.CachedTokens, rec.ReasoningTokens,
		rec.Latency.Milliseconds(), rec.TTFT.Milliseconds(), priceVersion, errKind,
		l.now().Unix())
	return err
}

// Summary 是一次查询的汇总。
type Summary struct {
	Calls  int
	Input  int
	Output int
	Cached int
	Cost   float64
}

// SessionSummary 汇总某个 turn 集合的用量与费用。
//
// 与 Report 一样按每条调用发生时的价格计算：改价不该改写历史花费。
func (l *Ledger) SessionSummary(ctx context.Context, turnIDs []string) (Summary, error) {
	if len(turnIDs) == 0 {
		return Summary{}, nil
	}
	var qb strings.Builder
	qb.WriteString(`SELECT provider, model, input_tokens, output_tokens, cached_tokens, created_at
	                FROM llm_calls WHERE turn_id IN (?`)
	args := make([]any, 0, len(turnIDs))
	args = append(args, turnIDs[0])
	for _, id := range turnIDs[1:] {
		qb.WriteString(", ?")
		args = append(args, id)
	}
	qb.WriteString(")")
	query := qb.String()

	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Summary{}, err
	}
	defer rows.Close()

	var sum Summary
	for rows.Next() {
		var provider, model string
		var in, out, cached, createdAt int64
		if err := rows.Scan(&provider, &model, &in, &out, &cached, &createdAt); err != nil {
			return Summary{}, err
		}
		sum.Calls++
		sum.Input += int(in)
		sum.Output += int(out)
		sum.Cached += int(cached)
		if fee, ok := l.ComputeAt(provider, model, int(in), int(out), int(cached),
			time.Unix(createdAt, 0)); ok {
			sum.Cost += fee
		}
	}
	return sum, rows.Err()
}

func (l *Ledger) loadPrices(ctx context.Context) error {
	rows, err := l.db.QueryContext(ctx,
		`SELECT provider, model, currency, price_in, price_out, price_cached, effective_from
		 FROM prices ORDER BY effective_from ASC`)
	if err != nil {
		return fmt.Errorf("load prices: %w", err)
	}
	defer rows.Close()

	l.mu.Lock()
	defer l.mu.Unlock()
	for rows.Next() {
		var p Price
		var eff int64
		if err := rows.Scan(&p.Provider, &p.Model, &p.Currency, &p.In, &p.Out, &p.Cached, &eff); err != nil {
			return err
		}
		p.EffectiveFrom = time.Unix(eff, 0)
		// 每个版本各占一个键：报表要按调用时间查当时的价格
		l.prices[priceKey(p.Provider, p.Model, p.EffectiveFrom)] = p
	}
	return rows.Err()
}

// PersistPrice 把价格写入 prices 表（幂等：同 provider/model/effective_from 覆盖）。
func (l *Ledger) PersistPrice(ctx context.Context, p Price) error {
	if p.Currency == "" {
		p.Currency = "CNY"
	}
	if p.EffectiveFrom.IsZero() {
		p.EffectiveFrom = l.now().Truncate(time.Second)
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO prices(provider, model, currency, price_in, price_out, price_cached, effective_from)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(provider, model, effective_from) DO UPDATE SET
		   currency = excluded.currency,
		   price_in = excluded.price_in,
		   price_out = excluded.price_out,
		   price_cached = excluded.price_cached`,
		p.Provider, p.Model, p.Currency, p.In, p.Out, p.Cached, p.EffectiveFrom.Unix())
	if err != nil {
		return err
	}
	l.SetPrice(p)
	return nil
}

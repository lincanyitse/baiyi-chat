package cost

import (
	"context"
	"testing"
	"time"
)

// TestPriceHistoryNotRewritten 是 FR-OPS-002 的核心断言：
// 改了今天的价格，上个月的花费不该跟着变。
func TestPriceHistoryNotRewritten(t *testing.T) {
	l := newLedger(t)
	ctx := context.Background()

	jan := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)

	// 一月：输入 1 元/百万，输出 2 元/百万
	if err := l.PersistPrice(ctx, Price{
		Provider: "p", Model: "m", In: 1, Out: 2, EffectiveFrom: jan,
	}); err != nil {
		t.Fatal(err)
	}
	// 二月涨价
	if err := l.PersistPrice(ctx, Price{
		Provider: "p", Model: "m", In: 10, Out: 20, EffectiveFrom: feb,
	}); err != nil {
		t.Fatal(err)
	}

	// 一月的调用：100 万输入 + 100 万输出，按当时价 = 1 + 2 = 3
	fee, ok := l.ComputeAt("p", "m", 1_000_000, 1_000_000, 0, jan.Add(24*time.Hour))
	if !ok {
		t.Fatal("应能找到一月生效的价格")
	}
	if fee != 3 {
		t.Errorf("一月费用应按一月价算（3），实际 %v", fee)
	}

	// 二月的调用按二月价 = 10 + 20 = 30
	fee, ok = l.ComputeAt("p", "m", 1_000_000, 1_000_000, 0, feb.Add(24*time.Hour))
	if !ok {
		t.Fatal("应能找到二月生效的价格")
	}
	if fee != 30 {
		t.Errorf("二月费用应按二月价算（30），实际 %v", fee)
	}

	// 三月再改价，不影晌前两个月
	mar := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	if err := l.PersistPrice(ctx, Price{
		Provider: "p", Model: "m", In: 100, Out: 200, EffectiveFrom: mar,
	}); err != nil {
		t.Fatal(err)
	}

	fee, _ = l.ComputeAt("p", "m", 1_000_000, 1_000_000, 0, jan.Add(24*time.Hour))
	if fee != 3 {
		t.Errorf("再改价后一月费用仍应是 3，实际 %v", fee)
	}
}

// TestPriceBeforeFirstVersion 早于所有版本时没有价格，不算 0 而算「未定价」。
func TestPriceBeforeFirstVersion(t *testing.T) {
	l := newLedger(t)
	jan := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	if err := l.PersistPrice(context.Background(), Price{
		Provider: "p", Model: "m", In: 1, Out: 1, EffectiveFrom: jan,
	}); err != nil {
		t.Fatal(err)
	}

	_, ok := l.ComputeAt("p", "m", 1000, 1000, 0, jan.Add(-24*time.Hour))
	if ok {
		t.Error("早于第一个版本时不该给出价格")
	}
}

// TestReportUsesHistoricalPrice 报表同样按记录时价。
func TestReportUsesHistoricalPrice(t *testing.T) {
	l := newLedger(t)
	ctx := context.Background()
	db := l.db

	jan := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)

	_ = l.PersistPrice(ctx, Price{Provider: "p", Model: "m", In: 1, Out: 0, EffectiveFrom: jan})
	_ = l.PersistPrice(ctx, Price{Provider: "p", Model: "m", In: 10, Out: 0, EffectiveFrom: feb})

	// 直接插一条一月的调用
	if _, err := db.ExecContext(ctx,
		`INSERT INTO llm_calls(id, turn_id, provider, model, input_tokens, output_tokens, cached_tokens, created_at)
		 VALUES ('c1', 't1', 'p', 'm', 1000000, 0, 0, ?)`, jan.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	rep, err := l.Report(ctx, jan, feb)
	if err != nil {
		t.Fatal(err)
	}
	if !almostEqual(rep.Cost, 1) {
		t.Errorf("一月调用应按一月价算（1），实际 %v", rep.Cost)
	}
}

func almostEqual(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

// TestPriceOfReturnsLatestAtNow 当前价查询要拿到最新一档。
func TestPriceOfReturnsLatestAtNow(t *testing.T) {
	l := newLedger(t)
	ctx := context.Background()

	past := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)
	_ = l.PersistPrice(ctx, Price{Provider: "p", Model: "m", In: 1, EffectiveFrom: past})
	_ = l.PersistPrice(ctx, Price{Provider: "p", Model: "m", In: 5, EffectiveFrom: recent})

	p, ok := l.PriceOf("p", "m")
	if !ok {
		t.Fatal("应找到当前价")
	}
	if p.In != 5 {
		t.Errorf("应取最新一档（5），实际 %v", p.In)
	}
}

// TestFuturePriceIgnoredByCurrentLookup 未来生效的价格不该被当前查询用上。
func TestFuturePriceIgnoredByCurrentLookup(t *testing.T) {
	l := newLedger(t)
	ctx := context.Background()

	now := time.Now()
	_ = l.PersistPrice(ctx, Price{Provider: "p", Model: "m", In: 1, EffectiveFrom: now.Add(-time.Hour)})
	_ = l.PersistPrice(ctx, Price{Provider: "p", Model: "m", In: 99, EffectiveFrom: now.Add(24 * time.Hour)})

	p, ok := l.PriceOf("p", "m")
	if !ok {
		t.Fatal("应找到当前价")
	}
	if p.In != 1 {
		t.Errorf("未来生效的价格不该生效（应为 1，实际 %v）", p.In)
	}
}

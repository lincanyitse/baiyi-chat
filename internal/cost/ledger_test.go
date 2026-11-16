package cost

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	infra "github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

func newLedger(t *testing.T) *Ledger {
	t.Helper()
	db, err := infra.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	l, err := NewLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestComputeWithPrice(t *testing.T) {
	l := newLedger(t)
	l.SetPrice(Price{Provider: "p", Model: "m", In: 2.0, Out: 8.0, Cached: 0.5})

	fee, ok := l.Compute("p", "m", 1_000_000, 500_000, 0)
	if !ok {
		t.Fatal("应能计算费用")
	}
	want := 2.0 + 4.0 // 1M in * 2 + 0.5M out * 8
	if fee != want {
		t.Errorf("fee = %v, 期望 %v", fee, want)
	}
}

func TestComputeWithCachedTokens(t *testing.T) {
	l := newLedger(t)
	l.SetPrice(Price{Provider: "p", Model: "m", In: 10.0, Out: 0, Cached: 1.0})

	// 1M 输入中 800k 命中缓存，200k 未命中
	fee, _ := l.Compute("p", "m", 1_000_000, 0, 800_000)
	want := 0.2*10.0 + 0.8*1.0
	if diff := fee - want; diff < -1e-9 || diff > 1e-9 {
		t.Errorf("fee = %v, 期望 %v", fee, want)
	}
}

func TestComputeWithoutPrice(t *testing.T) {
	l := newLedger(t)
	_, ok := l.Compute("unknown", "model", 100, 100, 0)
	if ok {
		t.Error("未配置价格时应返回 false")
	}
}

func TestRecordAndSummary(t *testing.T) {
	l := newLedger(t)
	ctx := context.Background()
	l.SetPrice(Price{Provider: "p", Model: "m", In: 1.0, Out: 2.0})

	recs := []Record{
		{TurnID: "t1", Provider: "p", Model: "m", InputTokens: 1000, OutputTokens: 500},
		{TurnID: "t1", Provider: "p", Model: "m", InputTokens: 2000, OutputTokens: 1000},
	}
	for _, r := range recs {
		if err := l.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	sum, err := l.SessionSummary(ctx, []string{"t1"})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Calls != 2 {
		t.Errorf("calls = %d, 期望 2", sum.Calls)
	}
	if sum.Input != 3000 || sum.Output != 1500 {
		t.Errorf("tokens = %d/%d", sum.Input, sum.Output)
	}
	if sum.Cost <= 0 {
		t.Error("费用应大于 0")
	}
}

func TestRecordError(t *testing.T) {
	l := newLedger(t)
	ctx := context.Background()
	err := l.Record(ctx, Record{
		TurnID: "t1", Provider: "p", Model: "m", ErrorKind: llm.ErrRateLimited,
	})
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := l.SessionSummary(ctx, []string{"t1"})
	if sum.Calls != 1 {
		t.Errorf("失败的调用也应记账，calls = %d", sum.Calls)
	}
}

func TestPersistPriceIsIdempotent(t *testing.T) {
	l := newLedger(t)
	ctx := context.Background()
	eff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	p := Price{Provider: "p", Model: "m", In: 1, Out: 2, EffectiveFrom: eff}
	if err := l.PersistPrice(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.In = 5
	if err := l.PersistPrice(ctx, p); err != nil {
		t.Fatal(err)
	}

	got, ok := l.PriceOf("p", "m")
	if !ok {
		t.Fatal("价格应存在")
	}
	if got.In != 5 {
		t.Errorf("同生效日期应覆盖，In = %v", got.In)
	}
}

func TestPriceSurvivesReload(t *testing.T) {
	db, err := infra.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	l1, _ := NewLedger(db)
	ctx := context.Background()
	if err := l1.PersistPrice(ctx, Price{
		Provider: "p", Model: "m", In: 3, Out: 6,
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	l2, err := NewLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := l2.PriceOf("p", "m")
	if !ok {
		t.Fatal("重新加载后价格应仍在")
	}
	if got.In != 3 || got.Out != 6 {
		t.Errorf("价格 = %+v", got)
	}
}

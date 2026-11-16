package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/secret"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestSecrets(t *testing.T) *secret.Store {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	s, err := secret.New(secret.Options{DB: db.DB, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// 环境变量优先于加密存储：用户显式设了变量，说明他知道自己在做什么。
func TestKeyResolverPrefersEnv(t *testing.T) {
	secrets := newTestSecrets(t)
	ctx := context.Background()
	if err := secrets.Put(ctx, "p", "from-store"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BAIYI_TEST_ENV_KEY", "from-env")
	resolve := keyResolver(ctx, secrets, discardLogger())

	got := resolve("p", config.Provider{APIKeyEnv: "BAIYI_TEST_ENV_KEY"})
	if got != "from-env" {
		t.Fatalf("应优先环境变量，得到 %q", got)
	}
}

// 没有环境变量时用加密存储。
func TestKeyResolverFallsBackToStore(t *testing.T) {
	secrets := newTestSecrets(t)
	ctx := context.Background()
	if err := secrets.Put(ctx, "p", "from-store"); err != nil {
		t.Fatal(err)
	}

	resolve := keyResolver(ctx, secrets, discardLogger())
	got := resolve("p", config.Provider{APIKeyEnv: "BAIYI_NOT_SET_ANYWHERE"})
	if got != "from-store" {
		t.Fatalf("应回退到加密存储，得到 %q", got)
	}
}

// 两者都没有时返回空串，不 panic。
func TestKeyResolverEmpty(t *testing.T) {
	secrets := newTestSecrets(t)
	resolve := keyResolver(context.Background(), secrets, discardLogger())
	if got := resolve("nope", config.Provider{}); got != "" {
		t.Fatalf("应返回空串，得到 %q", got)
	}
}

// 加密存储不可用（nil）时只认环境变量，不能崩。
func TestKeyResolverNilStore(t *testing.T) {
	t.Setenv("BAIYI_TEST_NIL_KEY", "env-only")
	resolve := keyResolver(context.Background(), nil, discardLogger())

	if got := resolve("p", config.Provider{APIKeyEnv: "BAIYI_TEST_NIL_KEY"}); got != "env-only" {
		t.Fatalf("应使用环境变量，得到 %q", got)
	}
	if got := resolve("p", config.Provider{}); got != "" {
		t.Fatalf("没有任何来源时应返回空串，得到 %q", got)
	}
}

package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	store "github.com/lincanyitse/baiyi-chat/internal/store/sqlite"
)

func newResetApp(t *testing.T) *App {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	app := &App{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	app.Config.DataDir = t.TempDir()
	return app
}

// TestResetSessionsKeepsAuthAndSecrets 是这条约束的核心断言：
// 清会话不能顺手把口令和密钥清掉，否则用户被锁在门外。
func TestResetSessionsKeepsAuthAndSecrets(t *testing.T) {
	app := newResetApp(t)
	ctx := context.Background()

	// 造：一个会话 + 一条口令 + 一条密钥
	repo := store.New(app.DB)
	if err := repo.CreateSession(ctx, session.Session{ID: "s1", Title: "要删的"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Exec(`INSERT INTO web_auth (id, password_hash, updated_at) VALUES ('default','hash',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Exec(`INSERT INTO provider_secrets (provider_name, key_encrypted, updated_at) VALUES ('p', x'00', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Exec(`INSERT INTO prices (provider, model, price_in, price_out, effective_from) VALUES ('p','m',1,2,1)`); err != nil {
		t.Fatal(err)
	}

	if _, err := app.ResetData(ctx, string(resetSessions)); err != nil {
		t.Fatal(err)
	}

	// 会话没了
	var n int
	if err := app.DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("会话应被清空，实际 %d 条", n)
	}

	// 但口令、密钥、价格都还在
	for _, q := range []string{
		`SELECT COUNT(*) FROM web_auth`,
		`SELECT COUNT(*) FROM provider_secrets`,
		`SELECT COUNT(*) FROM prices`,
	} {
		var c int
		if err := app.DB.QueryRow(q).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 1 {
			t.Errorf("%s 不该被清空，实际 %d 行", q, c)
		}
	}
}

// TestResetKnowledgeKeepsSessions 清知识库不该删对话。
func TestResetKnowledgeKeepsSessions(t *testing.T) {
	app := newResetApp(t)
	ctx := context.Background()

	repo := store.New(app.DB)
	if err := repo.CreateSession(ctx, session.Session{ID: "s1", Title: "保留"}); err != nil {
		t.Fatal(err)
	}

	if _, err := app.ResetData(ctx, string(resetKnowledge)); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := app.DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("清知识库不该删会话，实际 %d 条", n)
	}
}

// TestResetAllClearsBoth 全清要覆盖两个范围。
func TestResetAllClearsBoth(t *testing.T) {
	app := newResetApp(t)
	ctx := context.Background()

	repo := store.New(app.DB)
	if err := repo.CreateSession(ctx, session.Session{ID: "s1", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Exec(`INSERT INTO facts (id, subject, subject_key, predicate, predicate_key, value, value_key, status, confidence, importance, confirmations, source_type, source_ref, session_id, turn_id, valid_from, created_at, updated_at) VALUES ('f1','u','u','p','p','v','v','active',0.9,0.5,1,'explicit','','','',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Exec(`INSERT INTO web_auth (id, password_hash, updated_at) VALUES ('default','hash',1)`); err != nil {
		t.Fatal(err)
	}

	if _, err := app.ResetData(ctx, string(resetAll)); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{`SELECT COUNT(*) FROM sessions`, `SELECT COUNT(*) FROM facts`} {
		var n int
		if err := app.DB.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s 应被清空，实际 %d", q, n)
		}
	}

	// 口令仍在
	var a int
	if err := app.DB.QueryRow(`SELECT COUNT(*) FROM web_auth`).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if a != 1 {
		t.Error("全清也不该删口令")
	}
}

// TestResetUnknownScope 未知范围要报错，不能默认全删。
func TestResetUnknownScope(t *testing.T) {
	app := newResetApp(t)
	if _, err := app.ResetData(context.Background(), "nope"); err == nil {
		t.Fatal("未知范围应报错")
	}
}

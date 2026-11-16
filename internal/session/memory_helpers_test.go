package session_test

import (
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/memory"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	store "github.com/lincanyitse/baiyi-chat/internal/store/sqlite"
)

func newTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

func newMemoryHooks(t *testing.T, db *sqlite.DB) *session.MemoryHooks {
	t.Helper()
	repo := memory.NewRepo(db.DB)
	return &session.MemoryHooks{
		Repo:     repo,
		Recaller: memory.NewRecaller(repo, memory.RecallerOptions{}),
		Writer:   memory.NewWriter(repo),
		Extract:  memory.NewExtractor(),
		Decayer:  memory.NewDecayer(repo, memory.DecayerOptions{}),
		Episodic: true,
	}
}

func newTestService(t *testing.T, db *sqlite.DB, mem *session.MemoryHooks) *session.Service {
	t.Helper()
	return session.NewService(session.Options{
		Repo:   store.New(db),
		System: "测试",
		Memory: mem,
	})
}

package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/channel/web"
	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/memory"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

func TestMemoryAdminOfNilInterface(t *testing.T) {
	var missing web.MemoryAdmin = memoryAdminOf(nil)
	if missing != nil {
		t.Fatal("没有记忆组件时必须返回真正的 nil 接口")
	}
	var emptyRepo web.MemoryAdmin = memoryAdminOf(&session.MemoryHooks{})
	if emptyRepo != nil {
		t.Fatal("有 Hooks 但没有仓储时也必须是 nil 接口，不能把 nil 指针装进接口")
	}

	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	repo := memory.NewRepo(db.DB)
	admin := memoryAdminOf(&session.MemoryHooks{
		Repo:     repo,
		Recaller: memory.NewRecaller(repo, memory.RecallerOptions{}),
	})
	if admin == nil {
		t.Fatal("装配后应返回可用的管理接口")
	}
	if _, err := admin.ListFacts(context.Background(), "", 10); err != nil {
		t.Fatal(err)
	}
}

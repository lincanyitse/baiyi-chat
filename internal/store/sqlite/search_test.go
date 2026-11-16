package sqlite

// 会话搜索与分页的测试。
//
// 这几条断言对应两个真实缺陷：
//   1. LIKE 不转义时，搜「100%」会匹配所有标题
//   2. 只回一页数据而不给总数，前端永远不知道是不是到底了

import (
	"context"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/session"
)

func seed(t *testing.T, r *Repo, titles ...string) {
	t.Helper()
	ctx := context.Background()
	for i, title := range titles {
		s := session.Session{ID: "s" + string(rune('a'+i)), Title: title}
		if err := r.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
}

// timePtr 返回当前时间的指针，用于归档。
func timePtr() *time.Time {
	now := time.Now()
	return &now
}

func TestSearchSessionsByTitle(t *testing.T) {
	r, _ := newRepo(t)
	seed(t, r, "Go 并发模型", "Rust 所有权", "Go 泛型")

	page, err := r.SearchSessions(context.Background(), session.SessionQuery{Query: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("应匹配 2 个，实际 %d", len(page.Items))
	}
	if page.Total != 2 {
		t.Errorf("Total 应为 2，实际 %d", page.Total)
	}
}

// LIKE 通配符必须转义，否则用户输入的 % 会匹配一切。
func TestSearchSessionsEscapesWildcards(t *testing.T) {
	r, _ := newRepo(t)
	seed(t, r, "进度 100%", "普通会话", "另一个")

	page, err := r.SearchSessions(context.Background(), session.SessionQuery{Query: "100%"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("「100%%」应只匹配 1 个，实际 %d（未转义会匹配全部）", len(page.Items))
	}
	if page.Items[0].Title != "进度 100%" {
		t.Errorf("匹配错了: %q", page.Items[0].Title)
	}
}

func TestSearchSessionsPagination(t *testing.T) {
	r, _ := newRepo(t)
	seed(t, r, "会话一", "会话二", "会话三", "会话四", "会话五")

	first, err := r.SearchSessions(context.Background(), session.SessionQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 {
		t.Fatalf("第一页应 2 条，实际 %d", len(first.Items))
	}
	if first.Total != 5 {
		t.Errorf("Total 应是 5（不止本页），实际 %d", first.Total)
	}

	second, err := r.SearchSessions(context.Background(), session.SessionQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 {
		t.Fatalf("第二页应 2 条，实际 %d", len(second.Items))
	}
	// 两页不该有重复
	if first.Items[0].ID == second.Items[0].ID {
		t.Error("分页出现重复条目")
	}
}

func TestSearchSessionsExcludesArchived(t *testing.T) {
	r, _ := newRepo(t)
	ctx := context.Background()
	seed(t, r, "活跃", "要归档")

	if err := r.ArchiveSession(ctx, "sb", timePtr()); err != nil {
		t.Fatal(err)
	}

	page, err := r.SearchSessions(ctx, session.SessionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("默认不该返回已归档的，实际 %d 条", len(page.Items))
	}

	withArchived, err := r.SearchSessions(ctx, session.SessionQuery{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(withArchived.Items) != 2 {
		t.Errorf("include_archived 时应返回 2 条，实际 %d", len(withArchived.Items))
	}
}

func TestSearchSessionsEmptyQuery(t *testing.T) {
	r, _ := newRepo(t)
	seed(t, r, "一", "二")

	page, err := r.SearchSessions(context.Background(), session.SessionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Errorf("空查询应返回全部，实际 %d", len(page.Items))
	}
}

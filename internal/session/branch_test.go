package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	store "github.com/lincanyitse/baiyi-chat/internal/store/sqlite"
)

// seedBranchTree 构造一棵有分叉的消息树：
//
//	u1
//	 ├ a1
//	 │  └ u2
//	 │     └ a2a
//	 └ a1b            ← 在 u1 下重新生成
//	    └ u2b
//
// 返回各节点 ID。
func seedBranchTree(t *testing.T, repo *store.Repo, sessionID string) map[string]string {
	t.Helper()
	ctx := context.Background()
	base := time.Now()

	ids := map[string]string{}
	seed := func(key, parent, role string, seq int, text string) {
		id := "msg-" + key
		ids[key] = id
		parentID := ""
		if parent != "" {
			parentID = ids[parent]
		}
		if err := repo.AppendMessage(ctx, session.StoredMessage{
			ID:        id,
			SessionID: sessionID,
			Seq:       seq,
			ParentID:  parentID,
			Role:      llm.Role(role),
			Content:   []llm.Block{llm.NewTextBlock(text)},
			Trust:     llm.TrustUser,
			CreatedAt: base.Add(time.Duration(seq) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	seed("u1", "", "user", 1, "第一问")
	seed("a1", "u1", "assistant", 2, "回答一")
	seed("u2", "a1", "user", 3, "追问")
	seed("a2a", "u2", "assistant", 4, "追问的回答")
	seed("a1b", "u1", "assistant", 5, "回答一（重新生成）")
	seed("u2b", "a1b", "user", 6, "换分支后的追问")

	return ids
}

func TestHistoryFollowsParentChain(t *testing.T) {
	db := newTestDB(t)
	repo := store.New(db)
	svc := newTestService(t, db, nil)
	ctx := context.Background()

	sess, err := svc.CreateSession(ctx, "分支")
	if err != nil {
		t.Fatal(err)
	}
	ids := seedBranchTree(t, repo, sess.ID)

	// 默认 leaf = seq 最大的消息（u2b），它属于重新生成那条分支
	chain, err := svc.History(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := messageIDs(chain)
	want := []string{ids["u1"], ids["a1b"], ids["u2b"]}
	if !equalStrings(got, want) {
		t.Errorf("默认分支 = %v，期望 %v", got, want)
	}
}

func TestHistoryAtPicksBranch(t *testing.T) {
	db := newTestDB(t)
	repo := store.New(db)
	svc := newTestService(t, db, nil)
	ctx := context.Background()

	sess, _ := svc.CreateSession(ctx, "分支")
	ids := seedBranchTree(t, repo, sess.ID)

	cases := []struct {
		name string
		leaf string
		want []string
	}{
		{"原分支", ids["a2a"], []string{ids["u1"], ids["a1"], ids["u2"], ids["a2a"]}},
		{"新分支", ids["u2b"], []string{ids["u1"], ids["a1b"], ids["u2b"]}},
		{"中间节点", ids["a1"], []string{ids["u1"], ids["a1"]}},
		{"根节点", ids["u1"], []string{ids["u1"]}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chain, err := svc.HistoryAt(ctx, sess.ID, c.leaf)
			if err != nil {
				t.Fatal(err)
			}
			if got := messageIDs(chain); !equalStrings(got, c.want) {
				t.Errorf("chain = %v，期望 %v", got, c.want)
			}
		})
	}
}

// 两条分支必须共享前缀，且各自的分支节点互不出现。
func TestBranchesSharePrefixButDiverge(t *testing.T) {
	db := newTestDB(t)
	repo := store.New(db)
	svc := newTestService(t, db, nil)
	ctx := context.Background()

	sess, _ := svc.CreateSession(ctx, "分支")
	ids := seedBranchTree(t, repo, sess.ID)

	a, _ := svc.HistoryAt(ctx, sess.ID, ids["a2a"])
	b, _ := svc.HistoryAt(ctx, sess.ID, ids["u2b"])

	if len(a) == 0 || len(b) == 0 {
		t.Fatal("分支不应为空")
	}
	if a[0].ID != b[0].ID {
		t.Errorf("两条分支应共享根节点，得到 %s / %s", a[0].ID, b[0].ID)
	}
	if a[1].ID == b[1].ID {
		t.Errorf("两条分支在第二层应分叉，却都是 %s", a[1].ID)
	}
	// 原分支里不能出现重新生成的那条
	for _, m := range a {
		if m.ID == ids["a1b"] {
			t.Error("原分支不应包含重新生成的回答")
		}
	}
}

func TestMessageTreeReportsSiblings(t *testing.T) {
	db := newTestDB(t)
	repo := store.New(db)
	svc := newTestService(t, db, nil)
	ctx := context.Background()

	sess, _ := svc.CreateSession(ctx, "分支")
	ids := seedBranchTree(t, repo, sess.ID)

	tree, err := svc.MessageTreeOf(ctx, sess.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// a1 与 a1b 是兄弟：共 2 个，a1 第 1、a1b 第 2
	if n := tree.Nodes[ids["a1"]].Branch; n.Siblings != 2 || n.Index != 1 {
		t.Errorf("a1 分支位置 = %+v，期望 {2 1}", n)
	}
	if n := tree.Nodes[ids["a1b"]].Branch; n.Siblings != 2 || n.Index != 2 {
		t.Errorf("a1b 分支位置 = %+v，期望 {2 2}", n)
	}
	// u1 是根，自己就是唯一的根节点
	if n := tree.Nodes[ids["u1"]].Branch; n.Siblings != 1 || n.Index != 1 {
		t.Errorf("根节点分支位置 = %+v，期望 {1 1}", n)
	}
	// u1 有两个孩子（a1 与 a1b）
	if n := tree.Nodes[ids["u1"]].Children; n != 2 {
		t.Errorf("u1 子节点数 = %d，期望 2", n)
	}
	// 默认 leaf 是最新的 u2b
	if tree.Leaf != ids["u2b"] {
		t.Errorf("Leaf = %s，期望 %s", tree.Leaf, ids["u2b"])
	}
}

// BranchOptions 要同时给出每个兄弟与它各自的叶子，且只查一次库。
func TestBranchOptionsFollowsNewestChild(t *testing.T) {
	db := newTestDB(t)
	repo := store.New(db)
	svc := newTestService(t, db, nil)
	ctx := context.Background()

	sess, _ := svc.CreateSession(ctx, "分支")
	ids := seedBranchTree(t, repo, sess.ID)

	// a1 的兄弟是 a1 与 a1b；a1b 下面还挂着 u2b，叶子应是最深处
	opts, err := svc.BranchOptions(ctx, sess.ID, ids["a1"])
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 2 {
		t.Fatalf("应有 2 个分支选项，得到 %d", len(opts))
	}
	want := map[string]string{
		ids["a1"]:  ids["a2a"],
		ids["a1b"]: ids["u2b"],
	}
	for _, o := range opts {
		if got := want[o.Message.ID]; got != o.Leaf {
			t.Errorf("%s 的叶子 = %s，期望 %s", o.Message.ID, o.Leaf, got)
		}
	}
}

func messageIDs(msgs []llm.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

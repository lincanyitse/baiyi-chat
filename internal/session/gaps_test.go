package session_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/agent"
	"github.com/lincanyitse/baiyi-chat/internal/cost"
	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/provider/registry"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	store "github.com/lincanyitse/baiyi-chat/internal/store/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/testutil/upstream"
)

const summaryText = "## 决策与结论\n已定\n## 未决问题\n无\n## 实体与人名\n甲\n## 用户偏好与新事实\n无\n## 工具产出与句柄\n无\n## 用户显式要求\n无\n"

type harness struct {
	svc  *session.Service
	repo *store.Repo
	up   *upstream.Server
}

func newHarness(t *testing.T, window int) *harness {
	t.Helper()
	t.Setenv("BAIYI_TEST_KEY", "k")

	up := upstream.Start()
	t.Cleanup(up.Close)

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"fake": {
			Kind:      "openai-compat",
			BaseURL:   up.URL(),
			APIKeyEnv: "BAIYI_TEST_KEY",
			Models: map[string]config.Model{
				"fake-model": {ContextWindow: window, MaxOutput: 256, Tools: true},
			},
		},
	}
	cfg.Route = map[string]config.Route{
		"chat": {Candidates: []string{"fake/fake-model"}},
	}

	reg, err := registry.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := cost.NewLedger(db)
	if err != nil {
		t.Fatal(err)
	}

	repo := store.New(db)
	svc := session.NewService(session.Options{
		Repo:     repo,
		Registry: reg,
		Ledger:   ledger,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		System:   "测试",
		Budget:   agent.Budget{MaxSteps: 4, TurnTimeout: 10 * time.Second, StepTimeout: 5 * time.Second},
	})
	return &harness{svc: svc, repo: repo, up: up}
}

func sseText(content string) string {
	raw, err := json.Marshal(content)
	if err != nil {
		panic(err)
	}
	return "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":" + string(raw) + "},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":4,\"total_tokens\":12}}\n\n" +
		"data: [DONE]\n\n"
}

func TestAskSnapshotBranchSearchAndResume(t *testing.T) {
	h := newHarness(t, 100000)
	ctx := context.Background()

	sess, err := h.svc.CreateSession(ctx, "第一场")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateSession(ctx, "第二场"); err != nil {
		t.Fatal(err)
	}

	res, err := h.svc.Ask(ctx, sess.ID, "完成度 100%", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "你好") {
		t.Fatalf("回答 = %q", res.Text)
	}

	snap, err := h.svc.ContextSnapshotAt(ctx, res.TurnID, -1)
	if err != nil || snap.TurnID != res.TurnID || len(snap.Segments) == 0 {
		t.Fatalf("快照 = %+v err=%v", snap, err)
	}
	list, err := h.svc.ListContextSnapshots(ctx, sess.ID, 10)
	if err != nil || len(list) == 0 || list[0].ID != snap.ID {
		t.Fatalf("列表 = %+v err=%v", list, err)
	}
	byID, err := h.svc.ContextSnapshotByID(ctx, snap.ID)
	if err != nil || byID.ID != snap.ID {
		t.Fatalf("按 ID = %+v err=%v", byID, err)
	}
	segs, budget, step, err := h.svc.LatestContext(ctx, sess.ID)
	if err != nil || segs == "" || budget == "" || step < 0 {
		t.Fatalf("LatestContext segs=%q budget=%q step=%d err=%v", segs, budget, step, err)
	}

	hits, err := h.svc.SearchMessages(ctx, sess.ID, "100%", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("搜索 = %+v err=%v", hits, err)
	}
	sessions, err := h.svc.ListSessions(ctx, 10)
	if err != nil || len(sessions) < 2 {
		t.Fatalf("会话列表 = %+v err=%v", sessions, err)
	}

	sum, err := h.svc.SessionSummary(ctx, sess.ID)
	if err != nil || sum.Calls == 0 {
		t.Fatalf("用量 = %+v err=%v", sum, err)
	}

	msgs, err := h.repo.ListMessages(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var userID, asstID string
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			userID = m.ID
		case llm.RoleAssistant:
			asstID = m.ID
		}
	}
	if userID == "" || asstID == "" {
		t.Fatalf("消息角色不全：%+v", msgs)
	}

	edited, err := h.svc.EditMessageWith(ctx, sess.ID, session.EditOptions{MessageID: userID, Text: "改过的问题"}, nil, session.AskOptions{})
	if err != nil || edited.Text == "" {
		t.Fatalf("编辑 = %+v err=%v", edited, err)
	}
	if _, _, err := h.svc.EditMessage(ctx, sess.ID, session.EditOptions{MessageID: userID, Text: "  "}); err == nil {
		t.Fatal("空文本应被拒绝")
	}

	regen, err := h.svc.RegenerateWith(ctx, sess.ID, session.RegenerateOptions{MessageID: asstID}, nil, session.AskOptions{})
	if err != nil || regen.TurnID == "" {
		t.Fatalf("重新生成 = %+v err=%v", regen, err)
	}

	if err := h.repo.UpdateTurnStatus(ctx, res.TurnID, session.TurnRunning, "", nil); err != nil {
		t.Fatal(err)
	}
	found, ok, err := h.svc.ResumableTurn(ctx, sess.ID)
	if err != nil || !ok || found.ID == "" {
		t.Fatalf("可恢复 = %+v ok=%v err=%v", found, ok, err)
	}
	resumed, err := h.svc.ResumeTurn(ctx, sess.ID, found.ID, nil)
	if err != nil || resumed.Text == "" {
		t.Fatalf("续跑 = %+v err=%v", resumed, err)
	}

	title, err := h.svc.GenerateTitle(ctx, "帮我看看测试怎么写")
	if err != nil || title == "" {
		t.Fatalf("标题 = %q err=%v", title, err)
	}

	ref, err := h.svc.CurrentModel()
	if err != nil || ref.Model != "fake-model" {
		t.Fatalf("当前模型 = %+v err=%v", ref, err)
	}
	if got, ok := h.svc.SessionModel(sess); !ok || got.Model != "fake-model" {
		t.Fatalf("会话模型 = %+v ok=%v", got, ok)
	}
	if caps := h.svc.ModelCapabilities(ref); caps.ContextWindow == 0 {
		t.Fatalf("能力 = %+v", caps)
	}
	models, err := h.svc.ListModels(ctx, "fake")
	if err != nil || len(models) != 1 || models[0].ID != "fake-model" {
		t.Fatalf("模型列表 = %+v err=%v", models, err)
	}
	if !h.svc.ConfiguredModels("fake")["fake-model"] {
		t.Fatal("已配置模型里应有 fake-model")
	}
	if names := h.svc.ProviderNames(); len(names) != 1 || names[0] != "fake" {
		t.Fatalf("Provider = %v", names)
	}
	if len(h.svc.ProvidersHealth()) == 0 {
		t.Fatal("健康报告不应为空")
	}
	if h.svc.Skills() != nil || h.svc.SkillsRoot() == "" {
		t.Fatal("未配置技能时应为空")
	}
	if err := h.svc.ReloadSkills(); err == nil {
		t.Fatal("未配置技能目录应报错")
	}

	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"fake": {
			Kind:      "openai-compat",
			BaseURL:   h.up.URL(),
			APIKeyEnv: "BAIYI_TEST_KEY",
			Models: map[string]config.Model{
				"other-model": {ContextWindow: 8000, Tools: true},
			},
		},
	}
	cfg.Route = map[string]config.Route{
		"chat": {Candidates: []string{"fake/other-model"}},
	}
	next, err := registry.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.svc.SetRegistry(next)
	swapped, err := h.svc.CurrentModel()
	if err != nil || swapped.Model != "other-model" {
		t.Fatalf("热替换后 = %+v err=%v", swapped, err)
	}
	again, err := h.svc.Ask(ctx, sess.ID, "再来一次", nil)
	if err != nil || again.Text == "" {
		t.Fatalf("替换后提问 = %+v err=%v", again, err)
	}
}

func TestCompressLongHistory(t *testing.T) {
	h := newHarness(t, 4000)
	ctx := context.Background()
	h.up.SetChat(sseText(summaryText))

	sess, err := h.svc.CreateSession(ctx, "压缩")
	if err != nil {
		t.Fatal(err)
	}

	parent := ""
	body := strings.Repeat("这是一段需要被压缩的历史。", 30)
	for i := 1; i <= 21; i++ {
		id := "hist-" + pad(i)
		if err := h.repo.AppendMessage(ctx, session.StoredMessage{
			ID: id, SessionID: sess.ID, Seq: i, ParentID: parent,
			Role: llm.RoleUser, Content: []llm.Block{llm.NewTextBlock(body)},
			CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		parent = id
	}

	res, err := h.svc.Ask(ctx, sess.ID, "请根据以上内容继续", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "决策与结论") {
		t.Fatalf("回答 = %q", res.Text)
	}

	loaded, ok, err := h.svc.LoadSummary(ctx, sess.ID)
	if err != nil || !ok || !strings.Contains(loaded.Summary, "决策与结论") {
		t.Fatalf("压缩摘要 = %+v ok=%v err=%v", loaded, ok, err)
	}

	st := loaded
	st.Summary = summaryText + "更新"
	st.CoveredThroughSeq = 30
	if err := h.svc.SaveSummary(ctx, st); err != nil {
		t.Fatal(err)
	}
	again, ok, err := h.svc.LoadSummary(ctx, sess.ID)
	if err != nil || !ok || again.CoveredThroughSeq != 30 {
		t.Fatalf("更新后 = %+v ok=%v err=%v", again, ok, err)
	}
}

func pad(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

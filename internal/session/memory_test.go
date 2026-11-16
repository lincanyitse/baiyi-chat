package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/memory"
)

// TestCrossSessionMemory 是 M2.2 的核心验收：
// 会话 A 说的偏好，会话 B 无需提示即被使用。
//
// 断言的是**最终到达上下文的内容**，不是内存对象——M1 就发生过
// SkillLines 在 session 填了、agent 声明了，但构造 contextx.Input 时
// 忘了转发的断链，只有端到端断言才抓得住。
func TestCrossSessionMemory(t *testing.T) {
	db := newTestDB(t)
	mem := newMemoryHooks(t, db)
	svc := newTestService(t, db, mem)

	ctx := context.Background()

	// 会话 A：用户说出偏好
	sessA, err := svc.CreateSession(ctx, "会话A")
	if err != nil {
		t.Fatal(err)
	}
	svc.CaptureMemory(ctx, sessA.ID, "turn-a", "请记住：我用 Go 写后端", "好的，我记住了。")

	// 会话 B：新会话，只问了一个无关问题
	sessB, err := svc.CreateSession(ctx, "会话B")
	if err != nil {
		t.Fatal(err)
	}

	profile, _ := svc.RecallMemory(ctx, "今天天气怎么样")
	if profile == "" {
		t.Fatal("会话 B 应拿到画像卡")
	}
	if !strings.Contains(profile, "Go") {
		t.Fatalf("画像卡应含会话 A 说的偏好：%q", profile)
	}
	if !strings.Contains(profile, "后端") {
		t.Fatalf("画像卡内容不完整：%q", profile)
	}

	_ = sessB
}

// TestMemoryNotPollutedByExternalContent 验证外部内容永不进画像卡。
func TestMemoryNotPollutedByExternalContent(t *testing.T) {
	db := newTestDB(t)
	mem := newMemoryHooks(t, db)
	svc := newTestService(t, db, mem)
	ctx := context.Background()

	w, err := mem.Writer.Write(ctx, memory.Candidate{
		Subject: "用户", Predicate: "偏好", Value: "简洁",
		SourceType: memory.SourceTurn,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟从文档里读到的内容被误当作用户自述
	if _, err := mem.Writer.Write(ctx, memory.Candidate{
		Subject: "用户", Predicate: "密码", Value: "hunter2",
		SourceType: memory.SourceExternal,
	}); err != nil {
		t.Fatal(err)
	}

	profile, _ := svc.RecallMemory(ctx, "随便问问")
	if strings.Contains(profile, "hunter2") {
		t.Fatalf("外部内容污染了画像卡：%q", profile)
	}
	if !strings.Contains(profile, "简洁") {
		t.Fatalf("正常记忆应保留：%q", profile)
	}
	_ = w
}

// TestCaptureMemoryOnlyFromUserText 验证助手回复不作为记忆来源。
func TestCaptureMemoryOnlyFromUserText(t *testing.T) {
	db := newTestDB(t)
	mem := newMemoryHooks(t, db)
	svc := newTestService(t, db, mem)
	ctx := context.Background()

	sess, _ := svc.CreateSession(ctx, "s")
	// 助手回复里含「我是…」，但那是助手在复述文档，不是用户在自述
	svc.CaptureMemory(ctx, sess.ID, "t1", "介绍一下这个项目",
		"我是白泽，一个个人助理。我喜欢简洁。")

	facts, err := mem.Repo.ListFacts(ctx, memory.StatusActive, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range facts {
		if strings.Contains(f.Value, "白泽") || strings.Contains(f.Value, "简洁") {
			t.Fatalf("助手回复不该被抽成用户记忆：%+v", f)
		}
	}
}

// TestMemoryDisabledIsSilent 验证未配置时所有记忆功能静默跳过。
func TestMemoryDisabledIsSilent(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db, nil)
	ctx := context.Background()

	profile, episodes := svc.RecallMemory(ctx, "上次说的那个项目")
	if profile != "" || episodes != nil {
		t.Fatal("未配置记忆时应返回空")
	}

	// 不该 panic
	svc.CaptureMemory(ctx, "s1", "t1", "请记住：我喜欢简洁", "好的")
}

// TestEpisodeCreatedForSubstantialTurns 验证情节被记录并可按线索召回。
func TestEpisodeCreatedForSubstantialTurns(t *testing.T) {
	db := newTestDB(t)
	mem := newMemoryHooks(t, db)
	svc := newTestService(t, db, mem)
	ctx := context.Background()

	sess, _ := svc.CreateSession(ctx, "s")
	svc.CaptureMemory(ctx, sess.ID, "t1",
		"我们讨论一下容器部署的流程，包括镜像拉取、健康检查与回滚策略",
		"部署流程分为三步：拉取镜像、启动容器、检查健康端点。")

	eps, err := mem.Repo.RecentEpisodes(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) == 0 {
		t.Fatal("应记录一条情节")
	}
	if !strings.Contains(eps[0].Summary, "容器部署") {
		t.Fatalf("情节摘要应含对话要点：%q", eps[0].Summary)
	}

	// 带线索词的问题应召回它
	_, recalled := svc.RecallMemory(ctx, "上次说的容器部署")
	if len(recalled) == 0 {
		t.Fatal("应召回相关情节")
	}
}

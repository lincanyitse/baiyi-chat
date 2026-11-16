package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

// waitTurn 等一轮跑完并返回最终状态。
func (e *testEnv) waitTurn(t *testing.T, turnID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, body := e.do(t, "GET", "/api/turns/"+turnID, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("查询轮次失败: %d %s", resp.StatusCode, body)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if st, _ := out["status"].(string); st != "running" && st != "received" {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("轮次 %s 未在超时内结束", turnID)
	return nil
}

// startTurn 发起一轮并等它跑完，返回助手消息 ID。
func (e *testEnv) startTurn(t *testing.T, sessionID, text string) string {
	t.Helper()
	resp, body := e.do(t, "POST", "/api/sessions/"+sessionID+"/turns", map[string]string{"text": text})
	if resp.StatusCode != 202 {
		t.Fatalf("发起轮次失败: %d %s", resp.StatusCode, body)
	}
	var started struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.Unmarshal(body, &started); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(t, started.TurnID)
	return e.lastAssistantID(t, sessionID)
}

func (e *testEnv) lastAssistantID(t *testing.T, sessionID string) string {
	t.Helper()
	resp, body := e.do(t, "GET", "/api/sessions/"+sessionID+"/messages", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("拉取消息失败: %d %s", resp.StatusCode, body)
	}
	var msgs []MessageDTO
	if err := json.Unmarshal(body, &msgs); err != nil {
		t.Fatal(err)
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			return msgs[i].ID
		}
	}
	t.Fatal("找不到助手消息")
	return ""
}

func (e *testEnv) messages(t *testing.T, sessionID string) []MessageDTO {
	t.Helper()
	resp, body := e.do(t, "GET", "/api/sessions/"+sessionID+"/messages", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("拉取消息失败: %d %s", resp.StatusCode, body)
	}
	var msgs []MessageDTO
	if err := json.Unmarshal(body, &msgs); err != nil {
		t.Fatal(err)
	}
	return msgs
}

// storedMessages 直接从数据库读全部消息（不分分支）。
func (e *testEnv) storedMessages(t *testing.T) []struct {
	ID, ParentID, Role string
} {
	t.Helper()
	rows, err := e.db.Query(`SELECT id, COALESCE(parent_id,''), role FROM messages ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []struct{ ID, ParentID, Role string }
	for rows.Next() {
		var m struct{ ID, ParentID, Role string }
		if err := rows.Scan(&m.ID, &m.ParentID, &m.Role); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestRegenerateCreatesSiblingAnswer(t *testing.T) {
	up := &fakeUpstream{}
	env := newTestEnv(t, up)

	sess, err := env.svc.CreateSession(context.Background(), "重新生成")
	if err != nil {
		t.Fatal(err)
	}

	first := env.startTurn(t, sess.ID, "你好")
	if n := len(env.messages(t, sess.ID)); n != 2 {
		t.Fatalf("首轮应有 2 条消息，得到 %d", n)
	}

	turn, plan, err := env.svc.Regenerate(context.Background(), sess.ID, session.RegenerateOptions{
		MessageID: first,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.RunBranchTurn(context.Background(), sess.ID, plan, turn.ID,
		nil, session.AskOptions{}); err != nil {
		t.Fatal(err)
	}

	// 数据库里应有 3 条：1 条提问 + 2 条回答
	stored := env.storedMessages(t)
	if len(stored) != 3 {
		t.Fatalf("库中应有 3 条消息，得到 %d", len(stored))
	}

	var answers, users int
	var userID string
	for _, m := range stored {
		switch m.Role {
		case "assistant":
			answers++
			if m.ParentID != userID && userID != "" {
				t.Errorf("回答的父节点 %s 不是提问 %s", m.ParentID, userID)
			}
		case "user":
			users++
			userID = m.ID
		}
	}
	if users != 1 {
		t.Errorf("重新生成不应重复写入提问，用户消息数 = %d", users)
	}
	if answers != 2 {
		t.Errorf("应有 2 条回答，得到 %d", answers)
	}

	// 当前分支只包含新回答（leaf = 最新的助手消息）
	if n := len(env.messages(t, sess.ID)); n != 2 {
		t.Errorf("当前分支应有 2 条消息，得到 %d", n)
	}
}

func TestRegenerateRejectsUserMessage(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})
	sess, _ := env.svc.CreateSession(context.Background(), "x")

	// 先造一条用户消息
	env.startTurn(t, sess.ID, "你好")
	all := env.messages(t, sess.ID)
	var userID string
	for _, m := range all {
		if m.Role == "user" {
			userID = m.ID
		}
	}
	if userID == "" {
		t.Fatal("找不到用户消息")
	}

	if _, _, err := env.svc.Regenerate(context.Background(), sess.ID, session.RegenerateOptions{
		MessageID: userID,
	}); err == nil {
		t.Fatal("对用户消息重新生成应报错")
	} else if !strings.Contains(err.Error(), "助手") {
		t.Errorf("错误信息应说明只能重新生成助手回答，得到: %v", err)
	}
}

func TestEditMessageBranchesFromSameParent(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})
	sess, _ := env.svc.CreateSession(context.Background(), "编辑")

	env.startTurn(t, sess.ID, "第一问")
	env.startTurn(t, sess.ID, "第二问")

	all := env.messages(t, sess.ID)
	if len(all) != 4 {
		t.Fatalf("应有 4 条消息，得到 %d", len(all))
	}

	var secondUser MessageDTO
	for _, m := range all {
		if m.Role == "user" && strings.Contains(textOf(m), "第二问") {
			secondUser = m
		}
	}
	if secondUser.ID == "" {
		t.Fatal("找不到第二问")
	}

	// 编辑第二问：新消息挂在第一问回答之下，成为原第二问的兄弟
	if _, err := env.svc.EditMessageWith(context.Background(), sess.ID, session.EditOptions{
		MessageID: secondUser.ID,
		Text:      "第二问（改过）",
	}, nil, session.AskOptions{}); err != nil {
		t.Fatal(err)
	}

	// 库中应有 6 条：原 4 条 + 编辑后的提问 + 新回答
	if n := len(env.storedMessages(t)); n != 6 {
		t.Fatalf("编辑后库中应有 6 条消息，得到 %d", n)
	}

	// 默认分支（leaf = 最新）应看到编辑后的版本，而不是原第二问
	chain, err := env.svc.History(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, m := range chain {
		for _, blk := range m.Content {
			if t, ok := blk.(llm.TextBlock); ok {
				joined.WriteString(t.Text)
			}
		}
	}
	if !strings.Contains(joined.String(), "第二问（改过）") {
		t.Errorf("默认分支应包含编辑后的消息，实际: %s", joined.String())
	}
	// 编辑的是第二问，第一问及其回答仍应作为共享前缀存在
	if !strings.Contains(joined.String(), "第一问") {
		t.Errorf("默认分支应保留共享前缀「第一问」，实际: %s", joined.String())
	}
	// 原第二问不应出现——它被编辑后的版本取代了（但仍在库里作为兄弟）
	if strings.Contains(joined.String(), "第二问段") {
		t.Errorf("默认分支不应包含被编辑的原文，实际: %s", joined.String())
	}
}

func textOf(m MessageDTO) string {
	var b strings.Builder
	for _, blk := range m.Content {
		b.WriteString(blk.Text)
	}
	return b.String()
}

// 工具调用必须随助手消息一起落库，否则回看历史时看不到「做过什么」。
//
// 这里直接向服务注入一个桩工具：假上游只会回文本，不会自己发起调用，
// 所以用 StreamCallback 之外的路径构造不出 tool_use 块。改为验证
// 「有工具调用时会被写入消息」，走 agent 真实执行路径。
func TestToolCallsPersistedInHistory(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})
	sess, _ := env.svc.CreateSession(context.Background(), "工具落库")

	// 换成会发工具调用的假上游
	env.up.toolCalls = true
	env.startTurn(t, sess.ID, "现在几点")

	chain, err := env.svc.History(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var hasToolUse bool
	for _, m := range chain {
		for _, blk := range m.Content {
			if _, ok := blk.(llm.ToolUseBlock); ok {
				hasToolUse = true
			}
		}
	}
	if !hasToolUse {
		t.Error("助手消息里应保留 tool_use 块")
	}
}

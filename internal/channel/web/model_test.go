package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 会话级模型覆盖：指定后本轮应走该 provider，而不是路由。
func TestSessionModelOverride(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})
	ctx := context.Background()

	sess, err := env.svc.CreateSession(ctx, "模型切换")
	if err != nil {
		t.Fatal(err)
	}

	// 切到 fake/fake-model（测试环境唯一配好的 provider/model）
	if err := env.svc.SetSessionModel(ctx, sess.ID, "fake/fake-model"); err != nil {
		t.Fatal(err)
	}

	got, err := env.svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ModelRef != "fake/fake-model" {
		t.Errorf("ModelRef = %q，期望 fake/fake-model", got.ModelRef)
	}

	// 发一轮，验证真的用了这个模型
	env.startTurn(t, sess.ID, "你好")

	var model string
	if err := env.db.QueryRow(
		`SELECT model FROM llm_calls ORDER BY rowid DESC LIMIT 1`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != "fake-model" {
		t.Errorf("llm_calls.model = %q，期望 fake-model", model)
	}
}

// 清空覆盖后应回到路由默认。
func TestSessionModelOverrideCleared(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})
	ctx := context.Background()

	sess, _ := env.svc.CreateSession(ctx, "清空")
	if err := env.svc.SetSessionModel(ctx, sess.ID, "fake/fake-model"); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.SetSessionModel(ctx, sess.ID, ""); err != nil {
		t.Fatal(err)
	}

	got, _ := env.svc.GetSession(ctx, sess.ID)
	if got.ModelRef != "" {
		t.Errorf("清空后 ModelRef 应为空，得到 %q", got.ModelRef)
	}

	// 仍能正常对话（回落到路由）
	env.startTurn(t, sess.ID, "你好")
}

// 指定未配置的 provider：明确报错，不静默降级。
func TestSessionModelUnknownProvider(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})
	ctx := context.Background()

	sess, _ := env.svc.CreateSession(ctx, "错误模型")
	// 格式合法但 provider 不存在
	if err := env.svc.SetSessionModel(ctx, sess.ID, "nonexistent/model"); err != nil {
		t.Fatalf("设置时应只校验格式，得到错误: %v", err)
	}

	resp, body := env.do(t, "POST", "/api/sessions/"+sess.ID+"/turns",
		map[string]string{"text": "你好"})
	if resp.StatusCode == 202 {
		t.Fatal("指定未配置的 provider 时不应接受轮次")
	}
	if !strings.Contains(string(body), "未配置") {
		t.Errorf("错误信息应说明 provider 未配置，得到: %s", body)
	}
}

// 格式非法的 model ref 在设置时就该被拒绝。
func TestSessionModelInvalidRef(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})
	sess, _ := env.svc.CreateSession(context.Background(), "坏格式")

	resp, _ := env.do(t, "PATCH", "/api/sessions/"+sess.ID,
		map[string]string{"model": "no-slash-here"})
	if resp.StatusCode != 400 {
		t.Errorf("缺斜杠的 model ref 应 400，得到 %d", resp.StatusCode)
	}
}

// PATCH 接口能设置与清空模型。
func TestUpdateSessionModelEndpoint(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})
	sess, _ := env.svc.CreateSession(context.Background(), "接口")

	resp, body := env.do(t, "PATCH", "/api/sessions/"+sess.ID,
		map[string]string{"model": "fake/fake-model"})
	if resp.StatusCode != 200 {
		t.Fatalf("设置失败: %d %s", resp.StatusCode, body)
	}

	var dto SessionDTO
	if err := json.Unmarshal(body, &dto); err != nil {
		t.Fatal(err)
	}
	if dto.ModelRef != "fake/fake-model" {
		t.Errorf("响应 ModelRef = %q", dto.ModelRef)
	}

	// 清空
	resp2, body2 := env.do(t, "PATCH", "/api/sessions/"+sess.ID,
		map[string]string{"model": ""})
	if resp2.StatusCode != 200 {
		t.Fatalf("清空失败: %d", resp2.StatusCode)
	}
	var dto2 SessionDTO
	json.Unmarshal(body2, &dto2)
	if dto2.ModelRef != "" {
		t.Errorf("清空后应为空，得到 %q", dto2.ModelRef)
	}
}

// /api/models 返回的是「配置里声明过的」模型，带能力信息。
func TestListConfiguredModels(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})

	resp, body := env.do(t, "GET", "/api/models", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 %d: %s", resp.StatusCode, body)
	}

	var out struct {
		Models []struct {
			Ref    string `json:"ref"`
			Model  string `json:"model"`
			Tools  bool   `json:"tools"`
			Vision bool   `json:"vision"`
		} `json:"models"`
		Routes map[string][]string `json:"routes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}

	if len(out.Models) != 1 {
		t.Fatalf("应有 1 个已配置模型，得到 %d", len(out.Models))
	}
	m := out.Models[0]
	if m.Ref != "fake/fake-model" {
		t.Errorf("ref = %q", m.Ref)
	}
	if !m.Tools {
		t.Error("fake-model 声明了 tools=true")
	}
	if got := out.Routes["chat"]; len(got) != 1 || got[0] != "fake/fake-model" {
		t.Errorf("routes.chat = %v", got)
	}
}

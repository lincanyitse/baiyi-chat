package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/attach"
	"github.com/lincanyitse/baiyi-chat/internal/cost"
	"github.com/lincanyitse/baiyi-chat/internal/export"
	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/jobs"
	"github.com/lincanyitse/baiyi-chat/internal/memory"
	"github.com/lincanyitse/baiyi-chat/internal/rag"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	"github.com/lincanyitse/baiyi-chat/internal/skill"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

type memFiles map[string][]byte

func (m memFiles) ReadFile(path string) ([]byte, error) {
	b, ok := m[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b, nil
}

type fakeMCP struct{}

func (fakeMCP) Status() map[string]ServerStatus {
	return map[string]ServerStatus{
		"docs": {Connected: true, Tools: []string{"search"}},
	}
}

func (fakeMCP) Reconnect(_ context.Context, alias string) error {
	if alias == "missing" {
		return errors.New("没连上")
	}
	return nil
}

func (e *testEnv) upload(t *testing.T, path, filename string, data []byte) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, e.server.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func wantStatus(t *testing.T, env *testEnv, method, path string, body any, want int) []byte {
	t.Helper()
	resp, raw := env.do(t, method, path, body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s 状态码 = %d，期望 %d，正文 %s", method, path, resp.StatusCode, want, raw)
	}
	return raw
}

func TestHandlersWhenUnconfigured(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})

	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{"GET", "/api/kb/documents", nil, http.StatusNotFound},
		{"POST", "/api/kb/documents", nil, http.StatusNotFound},
		{"DELETE", "/api/kb/documents/x", nil, http.StatusNotFound},
		{"GET", "/api/kb/documents/x/chunks", nil, http.StatusNotFound},
		{"POST", "/api/kb/search", map[string]string{"query": "x"}, http.StatusNotFound},
		{"GET", "/api/jobs", nil, http.StatusOK},
		{"GET", "/api/memory/facts", nil, http.StatusNotFound},
		{"PATCH", "/api/memory/facts/x", map[string]string{"action": "confirm"}, http.StatusNotFound},
		{"GET", "/api/memory/facts/x/events", nil, http.StatusNotFound},
		{"GET", "/api/memory/card", nil, http.StatusNotFound},
		{"GET", "/api/memory/stats", nil, http.StatusNotFound},
		{"GET", "/api/sessions/x/export", nil, http.StatusNotFound},
		{"GET", "/api/export", nil, http.StatusNotFound},
		{"POST", "/api/backup", nil, http.StatusNotFound},
		{"GET", "/api/backups", nil, http.StatusNotFound},
		{"POST", "/api/attachments", nil, http.StatusNotFound},
		{"GET", "/api/attachments/x", nil, http.StatusNotFound},
		{"GET", "/api/mcp", nil, http.StatusNotFound},
		{"PUT", "/api/mcp", map[string]any{"servers": []any{}}, http.StatusNotFound},
		{"POST", "/api/mcp/a/reconnect", nil, http.StatusNotFound},
		{"GET", "/api/skills", nil, http.StatusNotFound},
		{"PATCH", "/api/skills/a", map[string]bool{"enabled": false}, http.StatusNotFound},
		{"POST", "/api/skills/reload", nil, http.StatusNotFound},
		{"GET", "/api/cost", nil, http.StatusNotFound},
		{"POST", "/api/reset", map[string]string{"scope": "sessions", "confirm": "sessions"}, http.StatusNotFound},
		{"POST", "/api/auth/change", map[string]string{"old": "old-password", "new": "new-password"}, http.StatusNotFound},
		{"POST", "/api/approvals/x", map[string]string{"action": "deny"}, http.StatusNotFound},
		{"GET", "/api/approvals/stream", nil, http.StatusNotFound},
		{"GET", "/api/approvals/pending", nil, http.StatusOK},
		{"GET", "/api/system", nil, http.StatusOK},
		{"GET", "/api/diagnostics", nil, http.StatusOK},
		{"GET", "/api/models", nil, http.StatusOK},
		{"GET", "/api/sessions/missing/search", nil, http.StatusBadRequest},
		{"GET", "/api/turns/missing/context?step=no", nil, http.StatusBadRequest},
		{"GET", "/api/turns/missing/context", nil, http.StatusNotFound},
		{"GET", "/api/turns/missing/context/diff", nil, http.StatusNotFound},
		{"GET", "/api/sessions/missing/context", nil, http.StatusNotFound},
		{"GET", "/api/sessions/missing/tree", nil, http.StatusNotFound},
		{"POST", "/api/messages/missing/regenerate", nil, http.StatusNotFound},
		{"PATCH", "/api/messages/missing", map[string]string{"text": ""}, http.StatusBadRequest},
		{"PATCH", "/api/messages/missing", map[string]string{"text": "改"}, http.StatusNotFound},
		{"GET", "/api/messages/missing/siblings", nil, http.StatusNotFound},
		{"POST", "/api/turns/missing/resume", nil, http.StatusNotFound},
	}
	for _, tc := range cases {
		wantStatus(t, env, tc.method, tc.path, tc.body, tc.want)
	}
}

func TestHandlersWhenConfigured(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(root, "alpha")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: alpha\ndescription: 阿尔法\n---\n\n正文\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cat := skill.NewCatalog(root)
	if _, err := cat.Refresh(); err != nil {
		t.Fatal(err)
	}

	env := newTestEnvOpt(t, &fakeUpstream{}, func(opts *Options, db *sqlite.DB) {
		repo := rag.NewRepo(db.DB)
		queue := jobs.NewStore(db.DB)
		opts.RAG = &RAGDeps{
			Repo:      repo,
			Ingester:  rag.NewIngester(repo, rag.NewParserRegistry(), rag.IngesterOptions{Queue: queue}),
			Retriever: rag.NewRetriever(repo, nil, nil, rag.RetrieverOptions{}),
			JobStore:  queue,
		}

		memRepo := memory.NewRepo(db.DB)
		if _, err := memRepo.InsertFact(context.Background(), memory.Fact{
			Subject: "用户", Predicate: "偏好", Value: "简洁回答",
			Status: memory.StatusActive, SourceType: memory.SourceExplicit,
			Confidence: 0.9, Importance: 0.8,
		}); err != nil {
			t.Fatal(err)
		}
		opts.Memory = memory.NewAdmin(memRepo, memory.NewRecaller(memRepo, memory.RecallerOptions{}))
		opts.Attachments = attach.NewStore(t.TempDir())
		opts.Skills = cat
		opts.ReloadSkills = func() error {
			_, err := cat.Refresh()
			return err
		}
		opts.MCP = fakeMCP{}
		opts.MCPServers = []config.MCPServer{{
			Alias: "docs", Transport: "http", URL: "http://127.0.0.1:9", Enabled: true,
		}}
		opts.SaveMCP = func(_ context.Context, servers []config.MCPServer) (config.Config, map[string]error, error) {
			return config.Config{MCP: servers}, map[string]error{}, nil
		}
		opts.ExportSession = func(_ context.Context, id string) (export.Doc, string, error) {
			if id == "missing" {
				return export.Doc{}, "", fmt.Errorf("会话不存在")
			}
			return export.Doc{
				Session: session.Session{ID: id, Title: "报告/草稿:1"},
			}, "报告/草稿:1", nil
		}
		opts.ListSessionsForExport = func(context.Context) ([]string, error) { return []string{"s1"}, nil }
		opts.Backup = func(context.Context) (BackupFile, error) {
			return BackupFile{Path: "b.bak", Size: 4, ModTime: time.Unix(100, 0)}, nil
		}
		opts.ListBackups = func() ([]BackupFile, error) {
			return []BackupFile{{Path: "b.bak", Size: 4, ModTime: time.Unix(100, 0)}}, nil
		}
		opts.ResetData = func(_ context.Context, scope string) (map[string]int, error) {
			return map[string]int{scope: 2}, nil
		}
		ledger, err := cost.NewLedger(db)
		if err != nil {
			t.Fatal(err)
		}
		opts.Ledger = ledger
		opts.SessionCost = func(context.Context, string) (cost.Summary, error) {
			return cost.Summary{Calls: 1, Input: 3}, nil
		}
		opts.Approvals = NewApprovalBroker(nil)
		opts.Version = "test"
		opts.DataDir = t.TempDir()
		opts.ConfigPath = "cfg.toml"
		opts.LogFile = "app.log"
		opts.Files = memFiles{
			"cfg.toml": []byte("api_key = \"sk-live-secret\"\nname = \"ok\"\n"),
			"app.log":  []byte("hello log\n"),
		}
	})

	// 知识库
	wantStatus(t, env, "DELETE", "/api/kb/documents/missing", nil, http.StatusNotFound)
	wantStatus(t, env, "POST", "/api/kb/search", map[string]string{"query": ""}, http.StatusBadRequest)
	resp, raw := env.upload(t, "/api/kb/documents", "note.md", []byte("# 白泽知识库\n\n一段可检索的正文。\n"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("上传 %d %s", resp.StatusCode, raw)
	}
	var uploaded struct {
		ID string `json:"id"`
	}
	if err := jsonUnmarshal(raw, &uploaded); err != nil || uploaded.ID == "" {
		t.Fatalf("文档 ID：%s %v", raw, err)
	}
	if !strings.Contains(string(wantStatus(t, env, "GET", "/api/kb/documents", nil, http.StatusOK)), uploaded.ID) {
		t.Fatal("列表里没有刚上传的文档")
	}
	wantStatus(t, env, "GET", "/api/kb/documents/"+uploaded.ID+"/chunks", nil, http.StatusOK)
	search := wantStatus(t, env, "POST", "/api/kb/search", map[string]any{"query": "白泽知识库", "limit": 5}, http.StatusOK)
	if !strings.Contains(string(search), "白泽") && !strings.Contains(string(search), "hits") {
		t.Fatalf("检索 = %s", search)
	}
	if !strings.Contains(string(wantStatus(t, env, "GET", "/api/jobs", nil, http.StatusOK)), "embed") {
		t.Fatal("上传后应有嵌入任务")
	}
	wantStatus(t, env, "DELETE", "/api/kb/documents/"+uploaded.ID, nil, http.StatusNoContent)

	// 记忆
	facts := wantStatus(t, env, "GET", "/api/memory/facts", nil, http.StatusOK)
	var factList []struct {
		ID string `json:"id"`
	}
	if err := jsonUnmarshal(facts, &factList); err != nil || len(factList) != 1 {
		t.Fatalf("事实 = %s %v", facts, err)
	}
	wantStatus(t, env, "PATCH", "/api/memory/facts/"+factList[0].ID, map[string]string{"action": "nope"}, http.StatusBadRequest)
	wantStatus(t, env, "PATCH", "/api/memory/facts/"+factList[0].ID, map[string]string{"action": "confirm"}, http.StatusOK)
	wantStatus(t, env, "GET", "/api/memory/facts/"+factList[0].ID+"/events", nil, http.StatusOK)
	wantStatus(t, env, "GET", "/api/memory/card", nil, http.StatusOK)
	wantStatus(t, env, "GET", "/api/memory/stats", nil, http.StatusOK)
	editRaw := wantStatus(t, env, "PATCH", "/api/memory/facts/"+factList[0].ID, map[string]string{"action": "edit", "value": "详细回答"}, http.StatusOK)
	if !strings.Contains(string(editRaw), "edit") {
		t.Fatalf("编辑响应 = %s", editRaw)
	}

	// 导出与备份
	wantStatus(t, env, "GET", "/api/sessions/missing/export", nil, http.StatusNotFound)
	wantStatus(t, env, "GET", "/api/sessions/s1/export?format=nope", nil, http.StatusBadRequest)
	expResp, expBody := env.do(t, "GET", "/api/sessions/s1/export", nil)
	if expResp.StatusCode != http.StatusOK {
		t.Fatalf("导出 %d %s", expResp.StatusCode, expBody)
	}
	name := dispositionName(t, expResp.Header.Get("Content-Disposition"))
	if name != "报告_草稿_1.md" || strings.Contains(name, "/") {
		t.Fatalf("文件名 = %q", name)
	}
	if !strings.Contains(string(expBody), "报告") {
		t.Fatalf("导出正文 = %s", expBody)
	}
	allResp, _ := env.do(t, "GET", "/api/export", nil)
	if allResp.StatusCode != http.StatusOK || allResp.Header.Get("Content-Type") == "" {
		t.Fatalf("全部导出 %d", allResp.StatusCode)
	}
	if !strings.Contains(allResp.Header.Get("Content-Disposition"), "jsonl") {
		t.Fatalf("全部导出头 = %s", allResp.Header.Get("Content-Disposition"))
	}
	if !strings.Contains(string(wantStatus(t, env, "POST", "/api/backup", nil, http.StatusOK)), "b.bak") {
		t.Fatal("备份响应缺少路径")
	}
	wantStatus(t, env, "GET", "/api/backups", nil, http.StatusOK)

	// 诊断脱敏
	diagResp, diagBody := env.do(t, "GET", "/api/diagnostics", nil)
	if diagResp.StatusCode != http.StatusOK {
		t.Fatalf("诊断 %d %s", diagResp.StatusCode, diagBody)
	}
	zr, err := zip.NewReader(bytes.NewReader(diagBody), int64(len(diagBody)))
	if err != nil {
		t.Fatal(err)
	}
	var cfgText string
	for _, f := range zr.File {
		if f.Name != "config.toml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		cfgText = string(b)
	}
	if strings.Contains(cfgText, "sk-live-secret") || !strings.Contains(cfgText, `***`) || !strings.Contains(cfgText, `name = "ok"`) {
		t.Fatalf("脱敏后的配置 = %q", cfgText)
	}
	if !strings.Contains(string(wantStatus(t, env, "GET", "/api/system", nil, http.StatusOK)), "test") {
		t.Fatal("系统信息缺少版本")
	}

	// 重置
	wantStatus(t, env, "POST", "/api/reset", map[string]string{"scope": "nope", "confirm": "nope"}, http.StatusBadRequest)
	wantStatus(t, env, "POST", "/api/reset", map[string]string{"scope": "sessions", "confirm": "no"}, http.StatusBadRequest)
	wantStatus(t, env, "POST", "/api/reset", map[string]string{"scope": "all", "confirm": "all"}, http.StatusOK)

	// 附件：空的 multipart 没有 file 字段
	empty := bytes.NewBuffer(nil)
	mw := multipart.NewWriter(empty)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	emptyReq, err := http.NewRequest(http.MethodPost, env.server.URL+"/api/attachments", empty)
	if err != nil {
		t.Fatal(err)
	}
	emptyReq.Header.Set("Content-Type", mw.FormDataContentType())
	emptyResp, err := http.DefaultClient.Do(emptyReq)
	if err != nil {
		t.Fatal(err)
	}
	emptyBody, _ := io.ReadAll(emptyResp.Body)
	emptyResp.Body.Close()
	if emptyResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺少文件应 400，实际 %d %s", emptyResp.StatusCode, emptyBody)
	}
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	upResp, upBody := env.upload(t, "/api/attachments", "dot.png", png)
	if upResp.StatusCode != http.StatusCreated || !strings.Contains(string(upBody), "image") {
		t.Fatalf("图片附件 %d %s", upResp.StatusCode, upBody)
	}
	var att struct {
		ID string `json:"id"`
	}
	if err := jsonUnmarshal(upBody, &att); err != nil || att.ID == "" {
		t.Fatal(err)
	}
	gotResp, gotBody := env.do(t, "GET", "/api/attachments/"+att.ID, nil)
	if gotResp.StatusCode != http.StatusOK || !bytes.Equal(gotBody, png) {
		t.Fatalf("取回附件 %d len=%d", gotResp.StatusCode, len(gotBody))
	}
	txtResp, txtBody := env.upload(t, "/api/attachments", "note.md", []byte("# 标题\n正文"))
	if txtResp.StatusCode != http.StatusCreated || !strings.Contains(string(txtBody), "document") {
		t.Fatalf("文本附件 %d %s", txtResp.StatusCode, txtBody)
	}
	badResp, _ := env.upload(t, "/api/attachments", "x.bin", []byte("PK\x03\x04not-supported"))
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("不支持的类型应 400，实际 %d", badResp.StatusCode)
	}

	// 技能与插件
	if !strings.Contains(string(wantStatus(t, env, "GET", "/api/skills", nil, http.StatusOK)), "alpha") {
		t.Fatal("技能列表缺少 alpha")
	}
	wantStatus(t, env, "PATCH", "/api/skills/nope", map[string]bool{"enabled": false}, http.StatusBadRequest)
	wantStatus(t, env, "PATCH", "/api/skills/alpha", map[string]bool{"enabled": false}, http.StatusOK)
	wantStatus(t, env, "POST", "/api/skills/reload", nil, http.StatusOK)
	if !strings.Contains(string(wantStatus(t, env, "GET", "/api/mcp", nil, http.StatusOK)), "docs") {
		t.Fatal("插件列表缺少 docs")
	}
	wantStatus(t, env, "PUT", "/api/mcp", map[string]any{"servers": []map[string]any{{"alias": ""}}}, http.StatusBadRequest)
	saved := wantStatus(t, env, "PUT", "/api/mcp", map[string]any{
		"servers": []map[string]any{{"alias": "docs", "transport": "http", "url": "http://127.0.0.1:9", "enabled": true}},
	}, http.StatusOK)
	if !strings.Contains(string(saved), "docs") {
		t.Fatalf("保存插件 = %s", saved)
	}
	wantStatus(t, env, "POST", "/api/mcp/missing/reconnect", nil, http.StatusBadGateway)
	wantStatus(t, env, "POST", "/api/mcp/docs/reconnect", nil, http.StatusOK)

	// 模型与成本
	if !strings.Contains(string(wantStatus(t, env, "GET", "/api/providers/fake/models", nil, http.StatusOK)), "fake-model") {
		t.Fatal("服务端模型列表缺少 fake-model")
	}
	wantStatus(t, env, "GET", "/api/providers/nope/models", nil, http.StatusBadGateway)
	wantStatus(t, env, "GET", "/api/models", nil, http.StatusOK)
	wantStatus(t, env, "GET", "/api/cost?range=forever", nil, http.StatusBadRequest)
	wantStatus(t, env, "GET", "/api/cost?range=today", nil, http.StatusOK)
	if !strings.Contains(string(wantStatus(t, env, "GET", "/api/cost?session=s1", nil, http.StatusOK)), `"calls":1`) {
		t.Fatal("会话成本不对")
	}

	// 审批
	wantStatus(t, env, "GET", "/api/approvals/pending", nil, http.StatusOK)
	wantStatus(t, env, "POST", "/api/approvals/x", map[string]string{"action": "nope"}, http.StatusBadRequest)
	wantStatus(t, env, "POST", "/api/approvals/x", map[string]string{"action": string(tool.Deny)}, http.StatusNotFound)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.server.URL+"/api/approvals/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("审批流 %d", streamResp.StatusCode)
	}

	// 对话周边：搜索、树、上下文、分支、续跑
	_, body := env.do(t, "POST", "/api/sessions", map[string]string{"title": "缺口"})
	var sess SessionDTO
	if err := jsonUnmarshal(body, &sess); err != nil || sess.ID == "" {
		t.Fatalf("建会话 %s %v", body, err)
	}
	turnID := startAndID(t, env, sess.ID, "完成度 100%")
	if !strings.Contains(string(wantStatus(t, env, "GET", "/api/sessions/"+sess.ID+"/search?q="+url.QueryEscape("100%"), nil, http.StatusOK)), "100%") {
		t.Fatal("会话搜索未命中")
	}
	wantStatus(t, env, "GET", "/api/sessions/"+sess.ID+"/tree", nil, http.StatusOK)
	if !strings.Contains(string(wantStatus(t, env, "GET", "/api/sessions/"+sess.ID+"/context", nil, http.StatusOK)), turnID) {
		t.Fatal("上下文列表缺少本轮")
	}
	wantStatus(t, env, "GET", "/api/turns/"+turnID+"/context", nil, http.StatusOK)
	wantStatus(t, env, "GET", "/api/turns/"+turnID+"/context/diff", nil, http.StatusOK)

	msgs := env.messages(t, sess.ID)
	var userID, asstID string
	for _, m := range msgs {
		if m.Role == "user" && userID == "" {
			userID = m.ID
		}
		if m.Role == "assistant" {
			asstID = m.ID
		}
	}
	wantStatus(t, env, "GET", "/api/messages/"+userID+"/siblings", nil, http.StatusOK)
	editResp, editBody := env.do(t, "PATCH", "/api/messages/"+userID, map[string]string{"text": "改过的问题"})
	if editResp.StatusCode != http.StatusAccepted {
		t.Fatalf("编辑 %d %s", editResp.StatusCode, editBody)
	}
	var edited struct {
		TurnID string `json:"turn_id"`
	}
	_ = jsonUnmarshal(editBody, &edited)
	env.waitTurn(t, edited.TurnID)

	regResp, regBody := env.do(t, "POST", "/api/messages/"+asstID+"/regenerate", nil)
	if regResp.StatusCode != http.StatusAccepted {
		t.Fatalf("重新生成 %d %s", regResp.StatusCode, regBody)
	}
	var regen struct {
		TurnID string `json:"turn_id"`
	}
	_ = jsonUnmarshal(regBody, &regen)
	env.waitTurn(t, regen.TurnID)

	wantStatus(t, env, "POST", "/api/turns/"+turnID+"/resume", nil, http.StatusAccepted)
}

func TestChangePassword(t *testing.T) {
	env := newTestEnvOpt(t, &fakeUpstream{}, func(opts *Options, db *sqlite.DB) {
		opts.Auth = NewAuth(db.DB)
	})

	resp, raw := env.do(t, "POST", "/api/auth/setup", map[string]string{"password": "correct-horse"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("设置口令 %d %s", resp.StatusCode, raw)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("设置口令后应签发会话")
	}

	resp, raw = env.do(t, "POST", "/api/auth/change", map[string]string{
		"old": "wrong-password", "new": "correct-horse-2",
	}, cookie)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("旧口令错误应 401，实际 %d %s", resp.StatusCode, raw)
	}
	resp, raw = env.do(t, "POST", "/api/auth/change", map[string]string{
		"old": "correct-horse", "new": "short",
	}, cookie)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("新口令过短应 400，实际 %d %s", resp.StatusCode, raw)
	}
	resp, raw = env.do(t, "POST", "/api/auth/change", map[string]string{
		"old": "correct-horse", "new": "correct-horse-2",
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("修改口令 %d %s", resp.StatusCode, raw)
	}
}

func startAndID(t *testing.T, env *testEnv, sessionID, text string) string {
	t.Helper()
	resp, body := env.do(t, "POST", "/api/sessions/"+sessionID+"/turns", map[string]string{"text": text})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("发起轮次 %d %s", resp.StatusCode, body)
	}
	var started struct {
		TurnID string `json:"turn_id"`
	}
	if err := jsonUnmarshal(body, &started); err != nil {
		t.Fatal(err)
	}
	env.waitTurn(t, started.TurnID)
	return started.TurnID
}

func dispositionName(t *testing.T, header string) string {
	t.Helper()
	const prefix = "attachment; filename*=UTF-8''"
	if !strings.HasPrefix(header, prefix) {
		t.Fatalf("Content-Disposition = %q", header)
	}
	name, err := url.PathUnescape(strings.TrimPrefix(header, prefix))
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func jsonUnmarshal(raw []byte, dest any) error {
	return json.Unmarshal(raw, dest)
}

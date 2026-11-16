package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/agent"
	"github.com/lincanyitse/baiyi-chat/internal/channel/web/stream"
	"github.com/lincanyitse/baiyi-chat/internal/cost"
	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/provider/registry"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	store "github.com/lincanyitse/baiyi-chat/internal/store/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
	"github.com/lincanyitse/baiyi-chat/internal/tool/builtin"
)

// fakeUpstream 是一个假的 OpenAI 兼容 SSE 服务。
//
// 用它而不是假 Provider：测试因此走真实的 openaicompat 适配器，
// 同时验证了 SSE 解析、事件归一化与渠道逻辑。
type fakeUpstream struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
	// blockCh 非 nil 时在最后几个分片前阻塞，用于测试取消。
	blockCh chan struct{}
	// toolCalls 为真时先发一次工具调用，再根据工具结果给最终回答。
	toolCalls bool
	// toolName 是要调用的工具名。
	toolName string
}

func (f *fakeUpstream) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"fake-model","owned_by":"test"}]}`))
			return
		}

		f.mu.Lock()
		f.calls++
		delay := f.delay
		blockCh := f.blockCh
		toolCalls := f.toolCalls
		toolName := f.toolName
		f.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)

		send := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if fl != nil {
				fl.Flush()
			}
		}

		send(`{"id":"x","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)

		// 第二轮请求（带着 tool 结果）时不再发起调用，直接回答
		if toolCalls && !requestHasToolResult(r) {
			if toolName == "" {
				toolName = "time_now"
			}
			send(fmt.Sprintf(`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":%q,"arguments":"{}"}}]},"finish_reason":null}]}`, toolName))
			send(`{"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
			send(`[DONE]`)
			return
		}

		if blockCh != nil {
			send(`{"id":"x","choices":[{"index":0,"delta":{"content":"开始生成…"},"finish_reason":null}]}`)
			select {
			case <-r.Context().Done():
				return
			case <-blockCh:
			}
		}

		for i := range 3 {
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-r.Context().Done():
					return
				}
			}
			send(fmt.Sprintf(`{"id":"x","choices":[{"index":0,"delta":{"content":"段%d"},"finish_reason":null}]}`, i))
		}
		send(`{"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
		send(`[DONE]`)
	})
}

// requestHasToolResult 判断请求体里是否已经带着工具结果。
func requestHasToolResult(r *http.Request) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	// 读完后放回去，避免影响后续（这里本来就不用再读）
	r.Body = io.NopCloser(bytes.NewReader(body))
	return bytes.Contains(body, []byte(`"tool_call_id"`)) ||
		bytes.Contains(body, []byte(`"role":"tool"`))
}

// testEnv 是一套完整的测试环境。
type testEnv struct {
	server *httptest.Server
	web    *Server
	svc    *session.Service
	up     *fakeUpstream
	db     *sql.DB
}

func newTestEnv(t *testing.T, up *fakeUpstream) *testEnv {
	t.Helper()
	return newTestEnvOpt(t, up, nil)
}

func newTestEnvOpt(t *testing.T, up *fakeUpstream, mutate func(*Options, *sqlite.DB)) *testEnv {
	t.Helper()

	// 假上游：一个最小的 OpenAI 兼容 SSE 服务
	upstream := httptest.NewServer(up.handler(t))
	t.Cleanup(upstream.Close)

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Providers["fake"] = config.Provider{
		Kind:      "openai-compat",
		BaseURL:   upstream.URL,
		APIKeyEnv: "BAIYI_TEST_KEY",
		Models: map[string]config.Model{
			"fake-model": {ContextWindow: 100000, MaxOutput: 4096, Tools: true},
		},
	}
	cfg.Route["chat"] = config.Route{Candidates: []string{"fake/fake-model"}}
	cfg.Route["tool_use"] = config.Route{Candidates: []string{"fake/fake-model"}}

	reg, err := registry.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ledger, err := cost.NewLedger(db)
	if err != nil {
		t.Fatal(err)
	}

	svc := session.NewService(session.Options{
		Repo:     store.New(db),
		Registry: reg,
		Ledger:   ledger,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		System:   "测试",
		Tools:    []tool.Tool{builtin.NewTimeNow()},
		Budget: agent.Budget{
			MaxSteps: 4, TurnTimeout: 10 * time.Second,
			StepTimeout: 5 * time.Second, MaxToolCalls: 5,
		},
	})

	opts := Options{
		Config:      config.WebConfig{Listen: "127.0.0.1:0"},
		Service:     svc,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Streams:     stream.NewManager(stream.ManagerOptions{}),
		BaseContext: context.Background(),
		Providers:   cfg.Providers,
		Routes:      cfg.Route,
	}
	if mutate != nil {
		mutate(&opts, db)
	}
	webSrv := New(opts)

	ts := httptest.NewServer(webSrv.Handler())
	t.Cleanup(ts.Close)

	return &testEnv{server: ts, web: webSrv, svc: svc, up: up, db: db.DB}
}

func (e *testEnv) do(t *testing.T, method, path string, body any, cookies ...*http.Cookie) (*http.Response, []byte) {
	t.Helper()

	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.server.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func TestHealth(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})

	resp, body := env.do(t, http.MethodGet, "/api/health", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 %d：%s", resp.StatusCode, body)
	}
	var h HealthDTO
	if err := json.Unmarshal(body, &h); err != nil {
		t.Fatal(err)
	}
	if h.Status != "ok" {
		t.Fatalf("健康状态错误：%+v", h)
	}
}

func TestSessionCRUD(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})

	// 创建
	resp, body := env.do(t, http.MethodPost, "/api/sessions", map[string]string{"title": "测试会话"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建失败 %d：%s", resp.StatusCode, body)
	}
	var created SessionDTO
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.Title != "测试会话" {
		t.Fatalf("标题错误：%q", created.Title)
	}

	// 列表
	resp, body = env.do(t, http.MethodGet, "/api/sessions", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表失败 %d", resp.StatusCode)
	}
	var list []SessionDTO
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 个会话，实际 %d", len(list))
	}

	// 重命名
	resp, body = env.do(t, http.MethodPatch, "/api/sessions/"+created.ID,
		map[string]string{"title": "改名后"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("重命名失败 %d：%s", resp.StatusCode, body)
	}

	// 归档
	resp, _ = env.do(t, http.MethodPatch, "/api/sessions/"+created.ID,
		map[string]bool{"archived": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("归档失败 %d", resp.StatusCode)
	}

	// 默认列表不含已归档
	_, body = env.do(t, http.MethodGet, "/api/sessions", nil)
	_ = json.Unmarshal(body, &list)
	if len(list) != 0 {
		t.Fatalf("默认不该含已归档会话，实际 %d", len(list))
	}

	// 显式包含
	_, body = env.do(t, http.MethodGet, "/api/sessions?include_archived=true", nil)
	_ = json.Unmarshal(body, &list)
	if len(list) != 1 {
		t.Fatalf("应含已归档会话，实际 %d", len(list))
	}

	// 删除
	resp, _ = env.do(t, http.MethodDelete, "/api/sessions/"+created.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("删除失败 %d", resp.StatusCode)
	}

	_, body = env.do(t, http.MethodGet, "/api/sessions?include_archived=true", nil)
	_ = json.Unmarshal(body, &list)
	if len(list) != 0 {
		t.Fatalf("删除后不该出现，实际 %d", len(list))
	}
}

func TestSessionNotFound(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})

	resp, body := env.do(t, http.MethodGet, "/api/sessions/nonexistent/messages", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("应返回 404，实际 %d：%s", resp.StatusCode, body)
	}
}

func TestStartTurnRequiresText(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})

	_, body := env.do(t, http.MethodPost, "/api/sessions", map[string]string{"title": "s"})
	var sess SessionDTO
	_ = json.Unmarshal(body, &sess)

	resp, body := env.do(t, http.MethodPost, "/api/sessions/"+sess.ID+"/turns",
		map[string]string{"text": ""})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空文本应返回 400，实际 %d：%s", resp.StatusCode, body)
	}
}

// TestChatEndToEnd 跑通「发消息 → 订阅 SSE → 收到完整流」。
func TestChatEndToEnd(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})

	_, body := env.do(t, http.MethodPost, "/api/sessions", map[string]string{"title": "聊天"})
	var sess SessionDTO
	_ = json.Unmarshal(body, &sess)

	resp, body := env.do(t, http.MethodPost, "/api/sessions/"+sess.ID+"/turns",
		map[string]string{"text": "你好"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("发起失败 %d：%s", resp.StatusCode, body)
	}
	var started struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.Unmarshal(body, &started); err != nil {
		t.Fatal(err)
	}
	if started.TurnID == "" {
		t.Fatal("应返回 turn_id")
	}

	// 订阅 SSE 直到 turn_finished
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		env.server.URL+"/api/turns/"+started.TurnID+"/events", nil)
	streamResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResp.Body.Close()

	raw, _ := io.ReadAll(streamResp.Body)
	text := string(raw)

	for _, want := range []string{"event: text_delta", "event: turn_finished"} {
		if !strings.Contains(text, want) {
			t.Fatalf("流中缺少 %q：\n%s", want, text)
		}
	}
	if !strings.Contains(text, "段0") || !strings.Contains(text, "段2") {
		t.Fatalf("流中缺少文本增量：\n%s", text)
	}
}

// TestStopTurn 验证停止按钮：取消后 turn 状态为 canceled。
func TestStopTurn(t *testing.T) {
	up := &fakeUpstream{blockCh: make(chan struct{})}
	env := newTestEnv(t, up)

	_, body := env.do(t, http.MethodPost, "/api/sessions", map[string]string{"title": "停止测试"})
	var sess SessionDTO
	_ = json.Unmarshal(body, &sess)

	_, body = env.do(t, http.MethodPost, "/api/sessions/"+sess.ID+"/turns",
		map[string]string{"text": "生成很长的回答"})
	var started struct {
		TurnID string `json:"turn_id"`
	}
	_ = json.Unmarshal(body, &started)

	// 等它真的开始跑
	waitFor(t, 2*time.Second, func() bool {
		return env.svc.IsTurnRunning(started.TurnID)
	})

	resp, body := env.do(t, http.MethodPost, "/api/turns/"+started.TurnID+"/stop", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("停止失败 %d：%s", resp.StatusCode, body)
	}
	var stopResp struct {
		Stopped bool `json:"stopped"`
	}
	_ = json.Unmarshal(body, &stopResp)
	if !stopResp.Stopped {
		t.Fatalf("应报告已停止：%s", body)
	}

	// 轮次应尽快退出
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		turn, err := env.svc.GetTurn(context.Background(), started.TurnID)
		if err == nil && turn.Status == session.TurnCanceled {
			return
		}
		if err == nil {
			t.Logf("等待中：status=%s err=%q", turn.Status, turn.Error)
		} else {
			t.Logf("GetTurn 失败：%v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("轮次未进入 canceled 状态（turnID=%q）", started.TurnID)
}

func TestStopFinishedTurnIsIdempotent(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})

	_, body := env.do(t, http.MethodPost, "/api/sessions", map[string]string{"title": "s"})
	var sess SessionDTO
	_ = json.Unmarshal(body, &sess)

	_, body = env.do(t, http.MethodPost, "/api/sessions/"+sess.ID+"/turns",
		map[string]string{"text": "短回答"})
	var started struct {
		TurnID string `json:"turn_id"`
	}
	_ = json.Unmarshal(body, &started)

	// 等它自然结束
	waitFor(t, 5*time.Second, func() bool {
		turn, err := env.svc.GetTurn(context.Background(), started.TurnID)
		return err == nil && turn.Status == session.TurnDone
	})

	resp, body := env.do(t, http.MethodPost, "/api/turns/"+started.TurnID+"/stop", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("对已结束轮次停止应幂等成功，实际 %d：%s", resp.StatusCode, body)
	}
}

func TestOriginCheckRejectsCrossSite(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})

	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/api/sessions",
		strings.NewReader(`{"title":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("跨站请求应被拒绝，实际 %d", resp.StatusCode)
	}
}

func TestOriginCheckAllowsSameSite(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})

	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/api/sessions",
		strings.NewReader(`{"title":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", env.server.URL)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("同源请求应放行，实际 %d", resp.StatusCode)
	}
}

func TestUnknownAPIPath(t *testing.T) {
	env := newTestEnv(t, &fakeUpstream{})
	resp, _ := env.do(t, http.MethodGet, "/api/does-not-exist", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知接口应 404，实际 %d", resp.StatusCode)
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

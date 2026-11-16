package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
)

// newAuthEnv 构造一个启用了口令的测试服务。
//
// 返回 *testEnv 而不是裸 server：认证测试仍要复用 do/messages 这些
// 已有的辅助，重写一份 HTTP 请求样板只会让两处慢慢分叉。
func newAuthEnv(t *testing.T, password string) (*testEnv, *Auth) {
	t.Helper()

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	auth := NewAuth(db.DB)
	if password != "" {
		if _, err := auth.Setup(password); err != nil {
			t.Fatal(err)
		}
	}

	base := newTestEnv(t, &fakeUpstream{})
	webSrv := New(Options{
		Config:      config.WebConfig{Listen: "127.0.0.1:0"},
		Service:     base.svc,
		Log:         base.web.opts.Log,
		Streams:     base.web.opts.Streams,
		BaseContext: base.web.opts.BaseContext,
		Auth:        auth,
	})
	base.server.Close()
	base.server = httptest.NewServer(webSrv.Handler())
	t.Cleanup(base.server.Close)
	base.web = webSrv
	return base, auth
}

// authReq 是 do 的便捷包装：带 Cookie 的请求。
func (e *testEnv) authReq(t *testing.T, method, path string, body any, cookie *http.Cookie) (*http.Response, []byte) {
	t.Helper()
	return e.do(t, method, path, body, cookie)
}

func sessionFrom(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

// 未登录时业务接口必须 401。
func TestAuthMiddlewareBlocksAPI(t *testing.T) {
	env, _ := newAuthEnv(t, "password123")

	resp, _ := env.authReq(t, "GET", "/api/sessions", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("未登录访问 /api/sessions 应 401，得到 %d", resp.StatusCode)
	}
}

// 认证接口本身必须放行，否则永远无法登录。
func TestAuthMiddlewareAllowsAuthEndpoints(t *testing.T) {
	env, _ := newAuthEnv(t, "password123")

	for _, path := range []string{"/api/auth/status", "/api/health"} {
		resp, _ := env.authReq(t, "GET", path, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s 应放行，得到 %d", path, resp.StatusCode)
		}
	}
}

// 登录成功后拿到的 Cookie 能访问业务接口。
func TestAuthLoginGrantsAccess(t *testing.T) {
	env, _ := newAuthEnv(t, "password123")

	resp, _ := env.authReq(t, "POST", "/api/auth/login",
		map[string]string{"password": "password123"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录应成功，得到 %d", resp.StatusCode)
	}
	cookie := sessionFrom(resp)
	if cookie == nil {
		t.Fatal("登录响应未下发 Cookie")
	}
	if !cookie.HttpOnly {
		t.Error("会话 Cookie 必须是 HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Error("会话 Cookie 应为 SameSite=Strict")
	}

	resp2, _ := env.authReq(t, "GET", "/api/sessions", nil, cookie)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("带 Cookie 访问应通过，得到 %d", resp2.StatusCode)
	}
}

// 错误口令不能换取 Cookie。
func TestAuthWrongPasswordRejected(t *testing.T) {
	env, _ := newAuthEnv(t, "password123")

	resp, _ := env.authReq(t, "POST", "/api/auth/login",
		map[string]string{"password": "wrong"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("错误口令应 401，得到 %d", resp.StatusCode)
	}
	if sessionFrom(resp) != nil {
		t.Error("失败登录不应下发 Cookie")
	}
}

// 伪造的 Cookie 不能通过。
func TestAuthForgedCookieRejected(t *testing.T) {
	env, _ := newAuthEnv(t, "password123")

	fake := &http.Cookie{Name: sessionCookie, Value: "forged-token-value"}
	resp, _ := env.authReq(t, "GET", "/api/sessions", nil, fake)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("伪造 Cookie 应 401，得到 %d", resp.StatusCode)
	}
}

// 未设置口令时不拦截——单人本机使用不该多一道门。
func TestAuthDisabledPassesThrough(t *testing.T) {
	env, _ := newAuthEnv(t, "")

	resp, _ := env.authReq(t, "GET", "/api/sessions", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("未启用口令时应直接放行，得到 %d", resp.StatusCode)
	}
}

// setup 只能在未设置口令时用一次。
func TestAuthSetupEndpoint(t *testing.T) {
	env, _ := newAuthEnv(t, "")

	resp, _ := env.authReq(t, "POST", "/api/auth/setup",
		map[string]string{"password": "password123"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("首次设置应成功，得到 %d", resp.StatusCode)
	}
	if sessionFrom(resp) == nil {
		t.Error("设置成功后应直接登录")
	}

	// 再设一次必须拒绝
	resp2, _ := env.authReq(t, "POST", "/api/auth/setup",
		map[string]string{"password": "another123"}, nil)
	if resp2.StatusCode == http.StatusOK {
		t.Error("重复设置应被拒绝")
	}
}

// 登出后 Cookie 立即失效。
func TestAuthLogoutEndpoint(t *testing.T) {
	env, _ := newAuthEnv(t, "password123")

	resp, _ := env.authReq(t, "POST", "/api/auth/login",
		map[string]string{"password": "password123"}, nil)
	cookie := sessionFrom(resp)

	resp2, _ := env.authReq(t, "POST", "/api/auth/logout", nil, cookie)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("登出应成功，得到 %d", resp2.StatusCode)
	}

	resp3, _ := env.authReq(t, "GET", "/api/sessions", nil, cookie)
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Errorf("登出后旧 Cookie 应失效，得到 %d", resp3.StatusCode)
	}
}

// 状态接口要如实反映登录态。
func TestAuthStatusReflectsLoginState(t *testing.T) {
	env, _ := newAuthEnv(t, "password123")

	_, body := env.authReq(t, "GET", "/api/auth/status", nil, nil)
	var status AuthStatusDTO
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.LoggedIn {
		t.Errorf("未登录时状态应为 enabled=true logged_in=false，得到 %+v", status)
	}

	loginResp, _ := env.authReq(t, "POST", "/api/auth/login",
		map[string]string{"password": "password123"}, nil)
	cookie := sessionFrom(loginResp)

	_, body2 := env.authReq(t, "GET", "/api/auth/status", nil, cookie)
	var status2 AuthStatusDTO
	json.Unmarshal(body2, &status2)
	if !status2.Enabled || !status2.LoggedIn {
		t.Errorf("登录后状态应为 logged_in=true，得到 %+v", status2)
	}
}

// Bearer 头也要能通过（供脚本使用）。
func TestAuthBearerToken(t *testing.T) {
	env, auth := newAuthEnv(t, "password123")
	token, err := auth.Login("password123")
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", env.server.URL+"/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Bearer token 应通过，得到 %d", resp.StatusCode)
	}
}

// 口令是运行期设置的：服务启动时还没有，中间件不能在那时就
// 判定「不需要认证」并永久放行。
func TestAuthEnabledAfterStartup(t *testing.T) {
	env, auth := newAuthEnv(t, "")

	// 启动时未启用，业务接口可直接访问
	resp, _ := env.authReq(t, "GET", "/api/sessions", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("未启用口令时应放行，得到 %d", resp.StatusCode)
	}

	// 运行期设置口令
	if _, err := auth.Setup("password123"); err != nil {
		t.Fatal(err)
	}

	// 之后必须立即要求认证
	resp2, _ := env.authReq(t, "GET", "/api/sessions", nil, nil)
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("设置口令后应立即拦截，得到 %d", resp2.StatusCode)
	}
}

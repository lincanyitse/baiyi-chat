package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
)

// newConfigEnv 构造一个可写配置的测试服务。
func newConfigEnv(t *testing.T, save func(map[string]config.Provider, map[string]config.Route) error) *httptest.Server {
	return newConfigEnvWithKeys(t, func(p map[string]config.Provider, r map[string]config.Route, _ map[string]string) error {
		return save(p, r)
	})
}

// newConfigEnvWithKeys 同上，但保存回调能拿到本次要写入的密钥。
func newConfigEnvWithKeys(t *testing.T, save func(map[string]config.Provider, map[string]config.Route, map[string]string) error) *httptest.Server {
	t.Helper()
	base := newTestEnv(t, &fakeUpstream{})

	webSrv := New(Options{
		Config:      config.WebConfig{Listen: "127.0.0.1:0"},
		Service:     base.svc,
		Log:         base.web.opts.Log,
		Streams:     base.web.opts.Streams,
		BaseContext: base.web.opts.BaseContext,
		Providers:   base.web.opts.Providers,
		Routes:      base.web.opts.Routes,
		SaveProviders: func(p map[string]config.Provider, r map[string]config.Route, keys map[string]string) (config.Config, error) {
			if err := save(p, r, keys); err != nil {
				return config.Config{}, err
			}
			return config.Config{Providers: p, Route: r}, nil
		},
	})
	ts := httptest.NewServer(webSrv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestProvidersEndpointExposesEditableFields(t *testing.T) {
	base := newTestEnv(t, &fakeUpstream{})
	cfg := config.Default()
	cfg.Providers["p"] = config.Provider{
		Kind:      "openai-compat",
		BaseURL:   "http://h/v1",
		APIKeyEnv: "MY_KEY",
		TimeoutS:  90,
		Models:    map[string]config.Model{"m": {ContextWindow: 1000, Tools: true}},
	}
	webSrv := New(Options{
		Config:      config.WebConfig{Listen: "127.0.0.1:0"},
		Service:     base.svc,
		Log:         base.web.opts.Log,
		Streams:     base.web.opts.Streams,
		BaseContext: base.web.opts.BaseContext,
		Providers:   cfg.Providers,
		Routes:      cfg.Route,
	})
	ts := httptest.NewServer(webSrv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 %d", resp.StatusCode)
	}

	var out struct {
		Providers []ProviderDTO `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Providers) != 1 {
		t.Fatalf("应有 1 个 provider，得到 %d", len(out.Providers))
	}

	p := out.Providers[0]
	// 编辑界面需要完整 URL 与环境变量名，否则保存会改坏配置
	if p.BaseURL != "http://h/v1" {
		t.Errorf("BaseURL 应保留完整路径，得到 %q", p.BaseURL)
	}
	if p.APIKeyEnv != "MY_KEY" {
		t.Errorf("APIKeyEnv 应返回，得到 %q", p.APIKeyEnv)
	}
	if p.TimeoutS != 90 {
		t.Errorf("TimeoutS 应返回，得到 %d", p.TimeoutS)
	}
}

func TestSaveProvidersEndpoint(t *testing.T) {
	var gotProviders map[string]config.Provider
	var gotRoutes map[string]config.Route
	ts := newConfigEnv(t, func(p map[string]config.Provider, r map[string]config.Route) error {
		gotProviders, gotRoutes = p, r
		return nil
	})

	body := ProvidersInput{
		Providers: []ProviderInput{{
			Name:      "newp",
			Kind:      "anthropic",
			BaseURL:   "http://x",
			APIKeyEnv: "K",
			TimeoutS:  30,
			Models:    []ModelDTO{{ID: "m1", ContextWindow: 1000, Tools: true}},
		}},
		Routes: map[string][]string{"chat": {"newp/m1"}},
	}

	resp, raw := plainReq(t, "PUT", ts.URL+"/api/providers", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存应成功，得到 %d: %s", resp.StatusCode, raw)
	}

	if gotProviders["newp"].Kind != "anthropic" {
		t.Errorf("provider 未传到保存函数: %+v", gotProviders)
	}
	if gotProviders["newp"].Models["m1"].ContextWindow != 1000 {
		t.Errorf("模型未传到保存函数: %+v", gotProviders["newp"].Models)
	}
	if len(gotRoutes["chat"].Candidates) != 1 || gotRoutes["chat"].Candidates[0] != "newp/m1" {
		t.Errorf("路由未传到保存函数: %+v", gotRoutes)
	}
}

func TestSaveProvidersRejectsMissingName(t *testing.T) {
	ts := newConfigEnv(t, func(map[string]config.Provider, map[string]config.Route) error {
		t.Fatal("校验失败时不该调用保存")
		return nil
	})

	resp, _ := plainReq(t, "PUT", ts.URL+"/api/providers", ProvidersInput{
		Providers: []ProviderInput{{Kind: "anthropic"}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("缺 name 应 400，得到 %d", resp.StatusCode)
	}
}

func TestSaveProvidersRejectsMissingModelID(t *testing.T) {
	ts := newConfigEnv(t, func(map[string]config.Provider, map[string]config.Route) error {
		t.Fatal("校验失败时不该调用保存")
		return nil
	})

	resp, _ := plainReq(t, "PUT", ts.URL+"/api/providers", ProvidersInput{
		Providers: []ProviderInput{{
			Name:   "p",
			Kind:   "anthropic",
			Models: []ModelDTO{{ContextWindow: 1}},
		}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("模型缺 id 应 400，得到 %d", resp.StatusCode)
	}
}

// 保存失败要把错误如实返回给界面。
func TestSaveProvidersPropagatesError(t *testing.T) {
	ts := newConfigEnv(t, func(map[string]config.Provider, map[string]config.Route) error {
		return os.ErrPermission
	})

	resp, raw := plainReq(t, "PUT", ts.URL+"/api/providers", ProvidersInput{
		Providers: []ProviderInput{{Name: "p", Kind: "anthropic"}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("保存失败应返回 4xx 携带原因，得到 %d", resp.StatusCode)
	}
	if len(raw) == 0 {
		t.Error("失败响应应带错误说明")
	}
}

// 没有配置文件时，保存接口要明确拒绝而不是静默成功。
func TestSaveProvidersUnavailable(t *testing.T) {
	base := newTestEnv(t, &fakeUpstream{})
	webSrv := New(Options{
		Config:      config.WebConfig{Listen: "127.0.0.1:0"},
		Service:     base.svc,
		Log:         base.web.opts.Log,
		Streams:     base.web.opts.Streams,
		BaseContext: base.web.opts.BaseContext,
	})
	ts := httptest.NewServer(webSrv.Handler())
	t.Cleanup(ts.Close)

	resp, _ := plainReq(t, "PUT", ts.URL+"/api/providers", ProvidersInput{})
	if resp.StatusCode == http.StatusOK {
		t.Error("未配置保存能力时不应返回成功")
	}
}

// 填了密钥就写进加密存储；留空表示不修改。
func TestSaveProvidersStoresKey(t *testing.T) {
	var gotKeys map[string]string
	ts := newConfigEnvWithKeys(t, func(p map[string]config.Provider, r map[string]config.Route, keys map[string]string) error {
		gotKeys = keys
		return nil
	})

	resp, raw := plainReq(t, "PUT", ts.URL+"/api/providers", ProvidersInput{
		Providers: []ProviderInput{{
			Name:   "p",
			Kind:   "anthropic",
			APIKey: "sk-secret",
		}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存应成功: %d %s", resp.StatusCode, raw)
	}
	if gotKeys["p"] != "sk-secret" {
		t.Errorf("密钥未传给保存函数: %+v", gotKeys)
	}
}

// 留空表示不修改已存的密钥，不能当作清除。
func TestSaveProvidersEmptyKeyKeepsExisting(t *testing.T) {
	var gotKeys map[string]string
	ts := newConfigEnvWithKeys(t, func(p map[string]config.Provider, r map[string]config.Route, keys map[string]string) error {
		gotKeys = keys
		return nil
	})

	resp, raw := plainReq(t, "PUT", ts.URL+"/api/providers", ProvidersInput{
		Providers: []ProviderInput{{Name: "p", Kind: "anthropic"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存应成功: %d %s", resp.StatusCode, raw)
	}
	if _, ok := gotKeys["p"]; ok {
		t.Errorf("留空不该产生密钥写入: %+v", gotKeys)
	}
}

// clear_key 才是显式清除。
func TestSaveProvidersClearKey(t *testing.T) {
	var gotKeys map[string]string
	ts := newConfigEnvWithKeys(t, func(p map[string]config.Provider, r map[string]config.Route, keys map[string]string) error {
		gotKeys = keys
		return nil
	})

	resp, raw := plainReq(t, "PUT", ts.URL+"/api/providers", ProvidersInput{
		Providers: []ProviderInput{{Name: "p", Kind: "anthropic", ClearKey: true}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存应成功: %d %s", resp.StatusCode, raw)
	}
	if v, ok := gotKeys["p"]; !ok || v != "" {
		t.Errorf("clear_key 应产生空密钥写入: %+v", gotKeys)
	}
}

// 密钥与环境变量名二选一，同时填是用户操作错误。
func TestSaveProvidersRejectsBothKeySources(t *testing.T) {
	ts := newConfigEnvWithKeys(t, func(map[string]config.Provider, map[string]config.Route, map[string]string) error {
		t.Fatal("校验失败时不该调用保存")
		return nil
	})

	resp, _ := plainReq(t, "PUT", ts.URL+"/api/providers", ProvidersInput{
		Providers: []ProviderInput{{
			Name:      "p",
			Kind:      "anthropic",
			APIKey:    "sk-x",
			APIKeyEnv: "MY_KEY",
		}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("同时填密钥与变量名应 400，得到 %d", resp.StatusCode)
	}
}

// GET /api/providers 要报告密钥状态，但绝不返回明文。
func TestListProvidersReportsKeyStateWithoutPlaintext(t *testing.T) {
	base := newTestEnv(t, &fakeUpstream{})
	fake := newFakeSecrets()
	_ = fake.Put(context.Background(), "withkey", "sk-must-not-leak")

	cfg := config.Default()
	cfg.Providers["withkey"] = config.Provider{Kind: "anthropic", Models: map[string]config.Model{}}
	cfg.Providers["nokey"] = config.Provider{Kind: "anthropic", Models: map[string]config.Model{}}

	webSrv := New(Options{
		Config:      config.WebConfig{Listen: "127.0.0.1:0"},
		Service:     base.svc,
		Log:         base.web.opts.Log,
		Streams:     base.web.opts.Streams,
		BaseContext: base.web.opts.BaseContext,
		Providers:   cfg.Providers,
		Routes:      cfg.Route,
		Secrets:     fake,
	})
	ts := httptest.NewServer(webSrv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(raw, []byte("sk-must-not-leak")) {
		t.Fatal("响应里出现了密钥明文")
	}

	var out struct {
		Providers []ProviderDTO `json:"providers"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]ProviderDTO, len(out.Providers))
	for _, p := range out.Providers {
		byName[p.Name] = p
	}

	if !byName["withkey"].HasKey || byName["withkey"].KeySource != "stored" {
		t.Errorf("已存密钥的 provider 状态不对: %+v", byName["withkey"])
	}
	if byName["nokey"].HasKey || byName["nokey"].KeySource != "none" {
		t.Errorf("没密钥的 provider 状态不对: %+v", byName["nokey"])
	}
}

// 环境变量优先：两者都有时报告 env，与运行时解析顺序一致。
func TestListProvidersKeySourcePrefersEnv(t *testing.T) {
	base := newTestEnv(t, &fakeUpstream{})
	fake := newFakeSecrets()
	_ = fake.Put(context.Background(), "p", "stored-key")

	t.Setenv("BAIYI_TEST_KEY", "env-key")
	cfg := config.Default()
	cfg.Providers["p"] = config.Provider{
		Kind: "anthropic", APIKeyEnv: "BAIYI_TEST_KEY", Models: map[string]config.Model{},
	}

	webSrv := New(Options{
		Config:      config.WebConfig{Listen: "127.0.0.1:0"},
		Service:     base.svc,
		Log:         base.web.opts.Log,
		Streams:     base.web.opts.Streams,
		BaseContext: base.web.opts.BaseContext,
		Providers:   cfg.Providers,
		Routes:      cfg.Route,
		Secrets:     fake,
	})
	ts := httptest.NewServer(webSrv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out struct {
		Providers []ProviderDTO `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Providers[0].KeySource != "env" {
		t.Errorf("环境变量应优先，得到 %q", out.Providers[0].KeySource)
	}
}

func TestResetSecretsEndpoint(t *testing.T) {
	base := newTestEnv(t, &fakeUpstream{})
	fake := newFakeSecrets()
	_ = fake.Put(context.Background(), "p", "k")

	webSrv := New(Options{
		Config:      config.WebConfig{Listen: "127.0.0.1:0"},
		Service:     base.svc,
		Log:         base.web.opts.Log,
		Streams:     base.web.opts.Streams,
		BaseContext: base.web.opts.BaseContext,
		Secrets:     fake,
	})
	ts := httptest.NewServer(webSrv.Handler())
	t.Cleanup(ts.Close)

	resp, raw := plainReq(t, "POST", ts.URL+"/api/providers/secrets/reset", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("重置应成功: %d %s", resp.StatusCode, raw)
	}
	if !fake.resetCalled {
		t.Error("重置接口没有调用 Reset")
	}
	if has, _ := fake.Has(context.Background(), "p"); has {
		t.Error("重置后不该还有密钥")
	}
}

func TestSecretsStatusReportsProtection(t *testing.T) {
	base := newTestEnv(t, &fakeUpstream{})
	webSrv := New(Options{
		Config:      config.WebConfig{Listen: "127.0.0.1:0"},
		Service:     base.svc,
		Log:         base.web.opts.Log,
		Streams:     base.web.opts.Streams,
		BaseContext: base.web.opts.BaseContext,
		Secrets:     newFakeSecrets(),
	})
	ts := httptest.NewServer(webSrv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/providers/secrets/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out struct {
		Available  bool   `json:"available"`
		Protection string `json:"protection"`
		Note       string `json:"note"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Available || out.Protection != "dpapi" {
		t.Errorf("状态不对: %+v", out)
	}
	if out.Note == "" {
		t.Error("应说明保护强度")
	}
}

// 加密存储不可用时，状态接口要明确说不可用而不是报错。
func TestSecretsStatusUnavailable(t *testing.T) {
	base := newTestEnv(t, &fakeUpstream{})
	webSrv := New(Options{
		Config:      config.WebConfig{Listen: "127.0.0.1:0"},
		Service:     base.svc,
		Log:         base.web.opts.Log,
		Streams:     base.web.opts.Streams,
		BaseContext: base.web.opts.BaseContext,
	})
	ts := httptest.NewServer(webSrv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/providers/secrets/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out struct {
		Available bool `json:"available"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Available {
		t.Error("没有 Secrets 时不该报告可用")
	}
}

// plainReq 是带 JSON body 的普通请求（配置测试不需要认证）。
func plainReq(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

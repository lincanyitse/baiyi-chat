package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTOML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("零配置应可加载: %v", err)
	}
	if cfg.DataDir == "" {
		t.Error("数据目录应有默认值")
	}
	if cfg.Log.Level != "info" {
		t.Errorf("默认日志级别 = %q", cfg.Log.Level)
	}
	if cfg.Session.MaxSteps != 12 {
		t.Errorf("默认最大步数 = %d", cfg.Session.MaxSteps)
	}
	if len(cfg.Route) != 0 || len(cfg.Providers) != 0 {
		t.Errorf("零配置不应预置路由或模型服务，route=%d providers=%d", len(cfg.Route), len(cfg.Providers))
	}
	if _, err = Load(""); err != nil {
		t.Fatalf("零配置应能通过校验: %v", err)
	}
}

func TestLoadFileOverridesDefaults(t *testing.T) {
	path := writeTOML(t, `
data_dir = "/custom/data"

[log]
level = "debug"
max_size_mb = 50

[session]
max_steps = 5
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/custom/data" {
		t.Errorf("data_dir = %q", cfg.DataDir)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("level = %q", cfg.Log.Level)
	}
	if cfg.Log.MaxSizeMB != 50 {
		t.Errorf("max_size_mb = %d", cfg.Log.MaxSizeMB)
	}
	if cfg.Session.MaxSteps != 5 {
		t.Errorf("max_steps = %d", cfg.Session.MaxSteps)
	}
	// 未覆盖的字段应保留默认
	if cfg.Session.TurnTimeoutS != 180 {
		t.Errorf("未覆盖字段应保留默认，实际 %d", cfg.Session.TurnTimeoutS)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	path := writeTOML(t, `
[log]
level = "warn"
`)
	t.Setenv("BAIYI_LOG__LEVEL", "debug")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("环境变量应覆盖文件，实际 %q", cfg.Log.Level)
	}
}

func TestDataDirEnvHighestPriority(t *testing.T) {
	path := writeTOML(t, `data_dir = "/from/file"`)
	t.Setenv("BAIYI_DATA_DIR", "/from/env")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/from/env" {
		t.Errorf("BAIYI_DATA_DIR 优先级最高，实际 %q", cfg.DataDir)
	}
}

func TestMissingFileIsNotError(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err != nil {
		t.Fatalf("配置文件不存在不应报错: %v", err)
	}
	if cfg.Log.Level != "info" {
		t.Error("应回退到默认值")
	}
}

func TestInvalidTOMLIsError(t *testing.T) {
	path := writeTOML(t, `this is not valid toml [[[`)
	if _, err := Load(path); err == nil {
		t.Fatal("非法 TOML 应报错")
	}
}

func TestValidateProviderKind(t *testing.T) {
	path := writeTOML(t, `
[providers.bad]
kind = "unknown"
base_url = "http://x"
api_key_env = "KEY"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("未知 kind 应报错")
	}
	if !contains(err.Error(), "unknown") {
		t.Errorf("错误信息应指明 kind: %v", err)
	}
}

func TestValidateProviderRequiresBaseURL(t *testing.T) {
	path := writeTOML(t, `
[providers.p]
kind = "openai-compat"
api_key_env = "KEY"
`)
	if _, err := Load(path); err == nil {
		t.Fatal("缺少 base_url 应报错")
	}
}

// 密钥可以来自加密存储（界面填写）或稍后再补，api_key_env 不是必填。
func TestValidateProviderAllowsMissingAPIKeyEnv(t *testing.T) {
	path := writeTOML(t, `
[providers.p]
kind = "openai-compat"
base_url = "http://x"
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("没有 api_key_env 不该阻止启动: %v", err)
	}
}

func TestAllProviderKindsAccepted(t *testing.T) {
	for _, kind := range []string{"openai-compat", "responses", "anthropic"} {
		path := writeTOML(t, `
[providers.p]
kind = "`+kind+`"
base_url = "http://x"
api_key_env = "KEY"
`)
		if _, err := Load(path); err != nil {
			t.Errorf("kind %q 应被接受: %v", kind, err)
		}
	}
}

func TestResolveAPIKey(t *testing.T) {
	t.Setenv("MY_TEST_KEY", "secret-value")

	p := Provider{APIKeyEnv: "MY_TEST_KEY"}
	if got := p.ResolveAPIKey(); got != "secret-value" {
		t.Errorf("ResolveAPIKey = %q", got)
	}

	missing := Provider{APIKeyEnv: "DEFINITELY_NOT_SET_XYZ"}
	if got := missing.ResolveAPIKey(); got != "" {
		t.Errorf("缺失时应返回空串，实际 %q", got)
	}

	none := Provider{}
	if got := none.ResolveAPIKey(); got != "" {
		t.Errorf("未配置时应返回空串，实际 %q", got)
	}
}

func TestProviderTimeout(t *testing.T) {
	if got := (&Provider{TimeoutS: 30}).Timeout().Seconds(); got != 30 {
		t.Errorf("Timeout = %v", got)
	}
	if got := (&Provider{}).Timeout().Seconds(); got != 120 {
		t.Errorf("默认超时 = %v, 期望 120", got)
	}
	if got := (&Provider{TimeoutS: -5}).Timeout().Seconds(); got != 120 {
		t.Errorf("负值应回退默认，实际 %v", got)
	}
}

func TestRouteParsing(t *testing.T) {
	path := writeTOML(t, `
[route.chat]
candidates = ["a/m1", "b/m2"]

[route.embed]
candidates = ["e/m3"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Route["chat"].Candidates; len(got) != 2 || got[0] != "a/m1" {
		t.Errorf("chat 路由 = %v", got)
	}
	if got := cfg.Route["embed"].Candidates; len(got) != 1 {
		t.Errorf("embed 路由 = %v", got)
	}
}

func TestModelCapabilities(t *testing.T) {
	path := writeTOML(t, `
[providers.p]
kind = "openai-compat"
base_url = "http://x"
api_key_env = "K"

[providers.p.models."m1"]
context_window = 64000
max_output = 2048
tools = true
vision = true
thinking = false
price_in = 1.5
price_out = 6.0
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Providers["p"].Models["m1"]
	if m.ContextWindow != 64000 || m.MaxOutput != 2048 {
		t.Errorf("窗口配置 = %+v", m)
	}
	if !m.Tools || !m.Vision || m.Thinking {
		t.Errorf("能力开关 = %+v", m)
	}
	if m.PriceIn != 1.5 || m.PriceOut != 6.0 {
		t.Errorf("价格 = %+v", m)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

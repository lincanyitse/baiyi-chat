package embed

import (
	"testing"
	"time"
)

func TestBuildNoneReturnsNil(t *testing.T) {
	for _, kind := range []string{"", "none"} {
		e, err := Build(Settings{Kind: kind})
		if err != nil || e != nil {
			t.Fatalf("kind=%q 应返回 nil：%v %v", kind, e, err)
		}
	}
}

func TestBuildFake(t *testing.T) {
	e, err := Build(Settings{Kind: "fake", Dims: 32, BatchSize: 8})
	if err != nil {
		t.Fatal(err)
	}
	if e == nil {
		t.Fatal("fake 后端不该为 nil")
	}
	if e.Dimensions("") != 32 {
		t.Fatalf("维度应为 32，实际 %d", e.Dimensions(""))
	}
}

func TestBuildOpenAIWrapsInLimiter(t *testing.T) {
	e, err := Build(Settings{Kind: "openai", BaseURL: "http://x", Model: "m", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.(*Limiter); !ok {
		t.Fatalf("应被 Limiter 包装，实际 %T", e)
	}
}

func TestBuildOllama(t *testing.T) {
	e, err := Build(Settings{Kind: "ollama", BaseURL: "http://x", Model: "bge-m3", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if e.Name() != "ollama" {
		t.Fatalf("名称错误：%s", e.Name())
	}
}

func TestBuildCustom(t *testing.T) {
	e, err := Build(Settings{Kind: "custom", BaseURL: "http://x", Model: "m", ResponsePath: "e"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Name() != "custom" {
		t.Fatalf("名称错误：%s", e.Name())
	}
}

func TestBuildUnknownKind(t *testing.T) {
	if _, err := Build(Settings{Kind: "量子"}); err == nil {
		t.Fatal("未知后端应报错")
	}
}

func TestBuildReranker(t *testing.T) {
	for _, kind := range []string{"", "none"} {
		r, err := BuildReranker(RerankSettings{Kind: kind})
		if err != nil || r != nil {
			t.Fatalf("kind=%q 应返回 nil：%v %v", kind, r, err)
		}
	}

	r, err := BuildReranker(RerankSettings{Kind: "fake"})
	if err != nil || r == nil {
		t.Fatalf("fake 重排应可用：%v %v", r, err)
	}
	if r.Name() != "fake" {
		t.Fatalf("名称错误：%s", r.Name())
	}

	r, err = BuildReranker(RerankSettings{Kind: "openai", BaseURL: "http://x", Model: "m", Timeout: time.Second})
	if err != nil || r == nil {
		t.Fatalf("openai 重排应可用：%v %v", r, err)
	}
}

func TestBuildRerankerUnknown(t *testing.T) {
	if _, err := BuildReranker(RerankSettings{Kind: "别的"}); err == nil {
		t.Fatal("未知重排后端应报错")
	}
}

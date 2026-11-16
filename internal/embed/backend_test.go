package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAIEmbedParsesAndOrdersByIndex(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("路径错误：%s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("鉴权头错误：%q", auth)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)

		// 故意打乱顺序，验证按 index 回填而不是按出现顺序
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float32{0, 1}},
				{"index": 0, "embedding": []float32{1, 0}},
			},
			"usage": map[string]any{"prompt_tokens": 42},
		})
	}))
	defer srv.Close()

	e := NewOpenAI(OpenAIOptions{BaseURL: srv.URL, APIKey: "test-key", Model: "bge-m3"})
	vecs, usage, err := e.Embed(context.Background(), "", []string{"甲", "乙"})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}

	if gotBody["model"] != "bge-m3" {
		t.Fatalf("请求未带默认模型：%v", gotBody["model"])
	}
	if len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("向量未按 index 回填：%v", vecs)
	}
	if usage.InputTokens != 42 || usage.Requests != 1 {
		t.Fatalf("用量错误：%+v", usage)
	}
}

func TestOpenAIEmbedNormalizes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"index": 0, "embedding": []float32{3, 4}}},
			"usage": map[string]any{"prompt_tokens": 1},
		})
	}))
	defer srv.Close()

	vecs, _, err := NewOpenAI(OpenAIOptions{BaseURL: srv.URL}).Embed(context.Background(), "m", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 2 || vecs[0][0] != 0.6 || vecs[0][1] != 0.8 {
		t.Fatalf("后端应归一化向量，实际 %v", vecs)
	}
}

func TestOpenAIEmbedCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0}}},
		})
	}))
	defer srv.Close()

	_, _, err := NewOpenAI(OpenAIOptions{BaseURL: srv.URL}).Embed(context.Background(), "m", []string{"a", "b"})
	if err == nil {
		t.Fatal("数量不符应当报错")
	}
}

func TestOpenAIServerErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"message":"模型正在加载"}}`))
	}))
	defer srv.Close()

	_, _, err := NewOpenAI(OpenAIOptions{BaseURL: srv.URL}).Embed(context.Background(), "m", []string{"a"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("5xx 应归为 ErrUnavailable，实际 %v", err)
	}
	// 错误消息必须保留厂商原文——ParseHTTPError 会消费 body，
	// 若在它之前读取过，这里就会是空的
	if !contains(err.Error(), "模型正在加载") {
		t.Fatalf("错误消息丢失了响应体：%v", err)
	}
}

func TestOpenAIBadRequestIsUnavailableToo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"模型不存在"}}`))
	}))
	defer srv.Close()

	_, _, err := NewOpenAI(OpenAIOptions{BaseURL: srv.URL}).Embed(context.Background(), "m", []string{"a"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("4xx 也应让调用方降级，实际 %v", err)
	}
}

func TestOpenAINetworkErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // 立即关闭，制造连接失败

	_, _, err := NewOpenAI(OpenAIOptions{BaseURL: url, Timeout: 200_000_000}).Embed(context.Background(), "m", []string{"a"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("连接失败应归为 ErrUnavailable，实际 %v", err)
	}
}

func TestOpenAIEmptyInputSkipsRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	vecs, usage, err := NewOpenAI(OpenAIOptions{BaseURL: srv.URL}).Embed(context.Background(), "m", nil)
	if err != nil || vecs != nil || usage.Requests != 0 {
		t.Fatalf("空输入应直接返回：%v %v %+v", vecs, usage, err)
	}
	if hits.Load() != 0 {
		t.Fatal("空输入不该发出请求")
	}
}

func TestOpenAIRerank(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Errorf("重排路径错误：%s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"index": 0, "relevance_score": 0.2},
				{"index": 1, "relevance_score": 0.9},
			},
			"usage": map[string]any{"total_tokens": 77},
		})
	}))
	defer srv.Close()

	e := NewOpenAI(OpenAIOptions{BaseURL: srv.URL})
	scores, usage, err := e.Rerank(context.Background(), "m", "查询", []string{"甲", "乙"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 2 || scores[0] != 0.2 || scores[1] != 0.9 {
		t.Fatalf("重排分数错误：%v", scores)
	}
	if usage.InputTokens != 77 {
		t.Fatalf("用量错误：%+v", usage)
	}
}

func TestOllamaEmbed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("路径错误：%s", r.URL.Path)
		}
		calls.Add(1)
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "bge-m3" {
			t.Errorf("模型错误：%s", req.Model)
		}
		// 按 prompt 长度返回不同向量，便于断言顺序
		v := []float32{1, float32(len(req.Prompt))}
		json.NewEncoder(w).Encode(map[string]any{"embedding": v})
	}))
	defer srv.Close()

	o := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "bge-m3"})
	vecs, usage, err := o.Embed(context.Background(), "", []string{"a", "bbb"})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("Ollama 单条接口应逐条请求，实际 %d 次", calls.Load())
	}
	if len(vecs) != 2 || len(vecs[0]) != 2 || len(vecs[1]) != 2 {
		t.Fatalf("向量数量错误：%v", vecs)
	}
	if usage.Requests != 2 {
		t.Fatalf("用量请求数错误：%+v", usage)
	}
}

func TestOllamaEmptyEmbeddingReportsMissingModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{}})
	}))
	defer srv.Close()

	_, _, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m"}).Embed(context.Background(), "", []string{"a"})
	if err == nil || !contains(err.Error(), "未拉取") {
		t.Fatalf("空向量应提示模型缺失，实际 %v", err)
	}
}

func TestCustomEmbedFollowsTemplateAndPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["q"] != `他说"你好"` {
			t.Errorf("文本未正确转义：%q", body["q"])
		}
		if body["m"] != "my-model" {
			t.Errorf("模型错误：%q", body["m"])
		}
		json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"vectors": []any{[]any{0.0, 2.0}}},
		})
	}))
	defer srv.Close()

	c := NewCustom(CustomOptions{
		URL:             srv.URL,
		Model:           "my-model",
		RequestTemplate: `{"m":"{{model}}","q":"{{text}}"}`,
		ResponsePath:    "result.vectors.0",
	})
	vecs, _, err := c.Embed(context.Background(), "", []string{`他说"你好"`})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 1 || vecs[0][1] != 1 {
		t.Fatalf("路径提取或归一化错误：%v", vecs)
	}
}

func TestCustomEmbedRootArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[1, 0, 0]`))
	}))
	defer srv.Close()

	c := NewCustom(CustomOptions{URL: srv.URL, ResponsePath: "-"})
	vecs, _, err := c.Embed(context.Background(), "m", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 1 || vecs[0][0] != 1 {
		t.Fatalf("根数组解析错误：%v", vecs)
	}
}

func TestCustomEmbedMissingPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"other":1}`))
	}))
	defer srv.Close()

	c := NewCustom(CustomOptions{URL: srv.URL, ResponsePath: "embedding"})
	_, _, err := c.Embed(context.Background(), "m", []string{"x"})
	if err == nil || !contains(err.Error(), "缺少字段") {
		t.Fatalf("缺字段应报错，实际 %v", err)
	}
}

func TestFakeIsDeterministicAndBounded(t *testing.T) {
	f := NewFake(64)
	a, _, err := f.Embed(context.Background(), "", []string{"白泽是一个个人助理"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := f.Embed(context.Background(), "", []string{"白泽是一个个人助理"})
	if Dot(a[0], b[0]) < 0.9999 {
		t.Fatalf("同样输入应得到同样向量，相似度 %v", Dot(a[0], b[0]))
	}
}

func TestFakePrefixSimilarity(t *testing.T) {
	f := NewFake(1024)
	vecs, _, _ := f.Embed(context.Background(), "", []string{
		"白泽是一个个人助理，可以记住你说过的话",
		"白泽是一个个人助理，可以记住你写过的字",
		"完全无关的天气预报说明天要下雨",
	})
	near := Dot(vecs[0], vecs[1])
	far := Dot(vecs[0], vecs[2])
	if near <= far {
		t.Fatalf("前缀相近的文本应更相似：近 %v，远 %v", near, far)
	}
}

func TestFakeUnavailable(t *testing.T) {
	f := NewFake(8)
	f.Unavailable = true
	_, _, err := f.Embed(context.Background(), "", []string{"x"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("应返回 ErrUnavailable，实际 %v", err)
	}
}

func TestFakeReranker(t *testing.T) {
	r := &FakeReranker{}
	scores, _, err := r.Rerank(context.Background(), "", "白泽助理", []string{
		"完全无关的内容",
		"白泽是个助理",
	})
	if err != nil {
		t.Fatal(err)
	}
	if scores[1] <= scores[0] {
		t.Fatalf("相关文档得分应更高：%v", scores)
	}
}

func TestFakeRerankerUnavailable(t *testing.T) {
	r := &FakeReranker{Unavailable: true}
	_, _, err := r.Rerank(context.Background(), "", "q", []string{"d"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("应返回 ErrUnavailable，实际 %v", err)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestOllamaTruncatesOversizedInput(t *testing.T) {
	var gotPrompt int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prompt string `json:"prompt"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotPrompt = len([]rune(req.Prompt))
		json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{1, 0}})
	}))
	defer srv.Close()

	long := strings.Repeat("中", 5000)
	_, _, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m"}).
		Embed(context.Background(), "", []string{long})
	if err != nil {
		t.Fatal(err)
	}
	if gotPrompt > maxOllamaRunes {
		t.Fatalf("超长输入应被截断到 %d，实际发出 %d 字", maxOllamaRunes, gotPrompt)
	}
	if gotPrompt == 0 {
		t.Fatal("不该把输入截成空")
	}
}

func TestOllamaContextOverflowIsNotRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"the input length exceeds the context length"}`))
	}))
	defer srv.Close()

	_, _, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m"}).
		Embed(context.Background(), "", []string{"x"})
	if !errors.Is(err, ErrUnsupportedInput) {
		t.Fatalf("输入超长应归为 ErrUnsupportedInput，实际 %v", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatal("输入超长不该被当成服务不可用")
	}
}

func TestLimiterDoesNotRetryUnsupportedInput(t *testing.T) {
	stub := &stubEmbedder{failFirst: 100, failKind: ErrUnsupportedInput}
	l := NewLimiter(stub, nil, LimiterOptions{MaxRetries: 5, Backoff: time.Millisecond})

	_, _, err := l.Embed(context.Background(), "m", []string{"x"})
	if !errors.Is(err, ErrUnsupportedInput) {
		t.Fatalf("应原样返回，实际 %v", err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("不该重试，实际调用 %d 次", got)
	}
}

func TestOpenAIEmbedReadsTotalTokens(t *testing.T) {
	// 千问等厂商用 total_tokens 而非 prompt_tokens。
	// 只读后者会让成本统计恒为 0，且不会报错。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"index": 0, "embedding": []float32{1, 0}}},
			"usage": map[string]any{"total_tokens": 77},
		})
	}))
	defer srv.Close()

	_, usage, err := NewOpenAI(OpenAIOptions{BaseURL: srv.URL}).
		Embed(context.Background(), "m", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 77 {
		t.Fatalf("应读 total_tokens，实际 %d", usage.InputTokens)
	}
}

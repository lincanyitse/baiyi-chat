// Package upstream 提供可脚本化的 OpenAI 兼容上游，供跨包测试使用。
//
// 只回放调用方给的 SSE 正文，不模拟工具循环或重试。协议适配器与事件
// 归一化仍然走生产代码。
package upstream

import (
	"net/http"
	"net/http/httptest"
	"sync"
)

// 最小的文本回复：一个内容分片、一个带 usage 的结束分片、然后 [DONE]。
const defaultChat = `data: {"id":"x","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}

data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}

data: [DONE]

`

const defaultModels = `{"data":[{"id":"fake-model","owned_by":"test","created":1}]}`

// Server 是一个本地 httptest 服务。
type Server struct {
	mu     sync.Mutex
	chat   string
	models string
	http   *httptest.Server
}

// Start 启动服务。chat 与 models 都用最小的合法响应。
func Start() *Server {
	s := &Server{chat: defaultChat, models: defaultModels}
	s.http = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Close 关闭服务。
func (s *Server) Close() { s.http.Close() }

// URL 是 Provider 的 BaseURL。
func (s *Server) URL() string { return s.http.URL }

// SetChat 替换下一次对话请求要回放的 SSE 正文。
func (s *Server) SetChat(sse string) {
	s.mu.Lock()
	s.chat = sse
	s.mu.Unlock()
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	chat, models := s.chat, s.models
	s.mu.Unlock()

	if len(r.URL.Path) >= len("/models") && r.URL.Path[len(r.URL.Path)-len("/models"):] == "/models" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(models))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(chat))
}

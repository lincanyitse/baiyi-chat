// Package provider 提供适配器之间共享的 HTTP 能力。
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// FetchModels 查询 OpenAI 生态的 GET <base>/models 并解析模型列表。
//
// baseURL 可以带或不带 /v1，也可以是完整的 .../chat/completions 或 .../responses
// 端点路径——这里统一归一化到 /v1/models。OpenAI 兼容与 Responses 适配器共用。
func FetchModels(ctx context.Context, client *http.Client, baseURL, apiKey string, headers map[string]string) ([]llm.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL(baseURL), nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, llm.AsError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, llm.ParseHTTPError(resp)
	}

	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("解析模型列表: %w", err)
	}

	out := make([]llm.ModelInfo, 0, len(payload.Data))
	for _, m := range payload.Data {
		out = append(out, llm.ModelInfo{ID: m.ID, OwnedBy: m.OwnedBy, Created: m.Created})
	}
	return out, nil
}

// modelsURL 把各种 base_url 写法归一化为 .../v1/models。
func modelsURL(baseURL string) string {
	base := strings.TrimSuffix(baseURL, "/")
	for _, suffix := range []string{"/chat/completions", "/responses"} {
		base = strings.TrimSuffix(base, suffix)
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/models"
}

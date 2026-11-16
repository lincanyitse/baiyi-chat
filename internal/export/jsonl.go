package export

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// jsonlSession 是 JSONL 的第一行：会话元数据。
type jsonlSession struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	ModelRef  string `json:"model_ref,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// jsonlMessage 是 JSONL 的后续行：一条消息。
//
// 字段与 StoredMessage 对齐，保留 parent_id 与原始 content 结构——
// 这是「将来能重新导入」的前提，不做任何有损转换。
type jsonlMessage struct {
	Type      string      `json:"type"`
	ID        string      `json:"id"`
	SessionID string      `json:"session_id"`
	Seq       int         `json:"seq"`
	ParentID  string      `json:"parent_id,omitempty"`
	Role      string      `json:"role"`
	Content   []llm.Block `json:"content"`
	Trust     string      `json:"trust,omitempty"`
	Pinned    bool        `json:"pinned,omitempty"`
	Compacted bool        `json:"compacted,omitempty"`
	CreatedAt string      `json:"created_at"`
}

// JSONL 把会话渲染成 JSONL：第一行元数据，之后每行一条消息。
func JSONL(doc Doc) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	head := jsonlSession{
		Type:      "session",
		ID:        doc.Session.ID,
		Title:     doc.Session.Title,
		ModelRef:  doc.Session.ModelRef,
		CreatedAt: doc.Session.CreatedAt.Format(time.RFC3339),
		UpdatedAt: doc.Session.UpdatedAt.Format(time.RFC3339),
	}
	if err := enc.Encode(head); err != nil {
		return nil, err
	}

	for _, m := range doc.Messages {
		row := jsonlMessage{
			Type:      "message",
			ID:        m.ID,
			SessionID: m.SessionID,
			Seq:       m.Seq,
			ParentID:  m.ParentID,
			Role:      string(m.Role),
			Content:   m.Content,
			Trust:     string(m.Trust),
			Pinned:    m.Pinned,
			Compacted: m.Compacted,
			CreatedAt: m.CreatedAt.Format(time.RFC3339),
		}
		if err := enc.Encode(row); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// JSONLAll 把多个会话渲染成一个 JSONL 文件：每个会话一段，
// 段首是元数据行，之后是该会话的消息。会话之间用元数据行天然分隔。
func JSONLAll(docs []Doc) ([]byte, error) {
	var buf bytes.Buffer
	for _, d := range docs {
		body, err := JSONL(d)
		if err != nil {
			return nil, err
		}
		buf.Write(body)
	}
	return buf.Bytes(), nil
}

// prettyJSON 把紧凑 JSON 变成缩进格式；解析失败时原样返回。
//
// 工具参数通常是紧凑 JSON，直接塞进 Markdown 代码块里读起来很累。
func prettyJSON(raw []byte) string {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}

package export

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

func mkMsg(id, parent string, seq int, role llm.Role, text string) session.StoredMessage {
	return session.StoredMessage{
		ID:        id,
		SessionID: "s1",
		Seq:       seq,
		ParentID:  parent,
		Role:      role,
		Content:   []llm.Block{llm.NewTextBlock(text)},
		Trust:     llm.TrustUser,
		CreatedAt: time.Now(),
	}
}

func mkSession() session.Session {
	return session.Session{
		ID: "s1", Title: "测试会话", ModelRef: "p/m",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func TestMarkdownBasic(t *testing.T) {
	doc := Doc{
		Session: mkSession(),
		Messages: []session.StoredMessage{
			mkMsg("a", "", 1, llm.RoleUser, "你好"),
			mkMsg("b", "a", 2, llm.RoleAssistant, "你好，有什么可以帮你"),
		},
	}
	got := Markdown(doc)

	for _, want := range []string{"# 测试会话", "用户", "你好", "助手", "有什么可以帮你", "p/m"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q：\n%s", want, got)
		}
	}
}

func TestMarkdownEmpty(t *testing.T) {
	got := Markdown(Doc{Session: mkSession()})
	if !strings.Contains(got, "# 测试会话") {
		t.Errorf("空会话也该输出标题: %s", got)
	}
}

func TestMarkdownUntitled(t *testing.T) {
	s := mkSession()
	s.Title = ""
	got := Markdown(Doc{Session: s})
	if !strings.Contains(got, "未命名会话") {
		t.Errorf("无标题应给默认名: %s", got)
	}
}

// 分支：主链在正文，被丢弃的分支进折叠块。
func TestMarkdownBranches(t *testing.T) {
	doc := Doc{
		Session: mkSession(),
		Messages: []session.StoredMessage{
			mkMsg("u1", "", 1, llm.RoleUser, "问题"),
			mkMsg("a1", "u1", 2, llm.RoleAssistant, "回答甲"),
			mkMsg("a2", "u1", 3, llm.RoleAssistant, "回答乙"), // 重新生成的结果
		},
	}
	got := Markdown(doc)

	// seq 更大的是主链（用户最后看到的）
	if !strings.Contains(got, "回答乙") {
		t.Error("主链应包含 seq 最大的回答")
	}
	if !strings.Contains(got, "未采用的分支") {
		t.Error("应标出未采用的分支")
	}
	if !strings.Contains(got, "回答甲") {
		t.Error("被丢弃的分支不该消失")
	}
	// 回答甲必须在折叠块里
	idxFold := strings.Index(got, "<summary>未采用的分支</summary>")
	idxA := strings.Index(got, "回答甲")
	if idxFold < 0 || idxA < idxFold {
		t.Errorf("回答甲应在折叠块内。fold=%d, a=%d", idxFold, idxA)
	}
}

func TestMarkdownToolCallFolded(t *testing.T) {
	m := session.StoredMessage{
		ID: "a", SessionID: "s1", Seq: 1, Role: llm.RoleAssistant,
		Content: []llm.Block{
			llm.NewTextBlock("我查一下"),
			llm.NewToolUseBlock("t1", "rag_search", json.RawMessage(`{"query":"x"}`)),
		},
		CreatedAt: time.Now(),
	}
	got := Markdown(Doc{Session: mkSession(), Messages: []session.StoredMessage{m}})

	if !strings.Contains(got, "调用工具：rag_search") {
		t.Errorf("应标出工具调用: %s", got)
	}
	if !strings.Contains(got, "<details>") {
		t.Error("工具调用应折叠")
	}
	if !strings.Contains(got, `"query": "x"`) {
		t.Errorf("工具参数应缩进显示: %s", got)
	}
}

func TestMarkdownLongToolResultTruncated(t *testing.T) {
	long := strings.Repeat("x", 5000)
	m := session.StoredMessage{
		ID: "a", SessionID: "s1", Seq: 1, Role: llm.RoleAssistant,
		Content: []llm.Block{
			llm.NewToolResultBlock("t1", []llm.Block{llm.NewTextBlock(long)}, false),
		},
		CreatedAt: time.Now(),
	}
	got := Markdown(Doc{Session: mkSession(), Messages: []session.StoredMessage{m}})
	if !strings.Contains(got, "已截断") {
		t.Error("超长工具结果应截断")
	}
	if len(got) > 4000 {
		t.Errorf("截断后不该还有 %d 字节", len(got))
	}
}

func TestJSONLRoundTrip(t *testing.T) {
	doc := Doc{
		Session: mkSession(),
		Messages: []session.StoredMessage{
			mkMsg("a", "", 1, llm.RoleUser, "你好"),
			mkMsg("b", "a", 2, llm.RoleAssistant, "在"),
		},
	}

	raw, err := JSONL(doc)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("应有 3 行（1 元数据 + 2 消息），实际 %d", len(lines))
	}

	var head map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatal(err)
	}
	if head["type"] != "session" || head["title"] != "测试会话" {
		t.Errorf("元数据行不对: %+v", head)
	}

	var msg map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &msg); err != nil {
		t.Fatal(err)
	}
	if msg["role"] != "assistant" || msg["parent_id"] != "a" {
		t.Errorf("消息行不对: %+v", msg)
	}
	if msg["type"] != "message" {
		t.Errorf("type 应为 message: %+v", msg)
	}
}

func TestJSONLPreservesStructure(t *testing.T) {
	m := session.StoredMessage{
		ID: "a", SessionID: "s1", Seq: 1, Role: llm.RoleAssistant,
		Content: []llm.Block{
			llm.NewTextBlock("文字"),
			llm.NewToolUseBlock("t1", "x", json.RawMessage(`{"k":1}`)),
		},
		Trust:     llm.TrustExternal,
		Pinned:    true,
		CreatedAt: time.Now(),
	}
	raw, err := JSONL(Doc{Session: mkSession(), Messages: []session.StoredMessage{m}})
	if err != nil {
		t.Fatal(err)
	}

	var row map[string]any
	if err := json.Unmarshal([]byte(strings.Split(strings.TrimSpace(string(raw)), "\n")[1]), &row); err != nil {
		t.Fatal(err)
	}

	content, ok := row["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("内容块应保留两个: %+v", row["content"])
	}
	first := content[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "文字" {
		t.Errorf("文本块被改坏: %+v", first)
	}
	if row["trust"] != "external" || row["pinned"] != true {
		t.Errorf("trust/pinned 应保留: %+v", row)
	}
}

func TestJSONLEmptySession(t *testing.T) {
	raw, err := JSONL(Doc{Session: mkSession()})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("空会话应只有元数据行，实际 %d 行", len(lines))
	}
}

func TestJSONLAll(t *testing.T) {
	d1 := Doc{Session: mkSession(), Messages: []session.StoredMessage{
		mkMsg("a", "", 1, llm.RoleUser, "一"),
	}}
	s2 := mkSession()
	s2.ID = "s2"
	d2 := Doc{Session: s2, Messages: []session.StoredMessage{
		mkMsg("b", "", 1, llm.RoleUser, "二"),
	}}

	raw, err := JSONLAll([]Doc{d1, d2})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 4 {
		t.Fatalf("两个会话应共 4 行，实际 %d", len(lines))
	}
}

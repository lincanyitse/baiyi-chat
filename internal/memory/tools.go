package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// Tools 提供记忆相关的模型工具。
type Tools struct {
	repo     *Repo
	recaller *Recaller
	writer   *Writer
	// sessionID / turnID 在工具执行时由 ExecContext 提供；
	// 这里的默认值用于无会话上下文的场景。
}

// NewTools 构造工具集。
func NewTools(repo *Repo, recaller *Recaller, writer *Writer) *Tools {
	return &Tools{repo: repo, recaller: recaller, writer: writer}
}

// MemoryWriteTool 让模型主动记住一件事。
type MemoryWriteTool struct{ t *Tools }

// NewMemoryWriteTool 构造 memory_write 工具。
func (t *Tools) MemoryWriteTool() *MemoryWriteTool { return &MemoryWriteTool{t: t} }

// Spec 实现 tool.Tool。
//
// 风险等级为 RiskRead：记忆写入只影响本地记忆库，不产生外部副作用，
// 且用户随时可以 /memory rm 撤销。把它当成需要审批的写操作会让
// 「记住我的偏好」变成一个要按 y 的弹窗——实际使用中极其烦人，
// 而误记的代价远小于误删文件。
func (*MemoryWriteTool) Spec() tool.Spec {
	return tool.DefaultSpec(tool.Spec{
		Name: "memory_write",
		Description: "记住一条用户的长期信息（偏好、身份、习惯、长期项目）。" +
			"只在用户明确表达且以后还会用到时使用；不要记录临时上下文、" +
			"一次性的任务细节或从文档里读到的内容。",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"subject": {"type": "string", "description": "主体，通常是「用户」"},
				"predicate": {"type": "string", "description": "属性名，如「偏好」「职业」「常用工具」"},
				"value": {"type": "string", "description": "具体内容"},
				"explicit": {"type": "boolean", "description": "是否为用户明确要求记住"}
			},
			"required": ["subject", "predicate", "value"],
			"additionalProperties": false
		}`),
		Risk: tool.RiskRead,
	})
}

// Execute 实现 tool.Tool。
func (m *MemoryWriteTool) Execute(ctx context.Context, raw json.RawMessage, ec *tool.ExecContext) (tool.Result, error) {
	var in struct {
		Subject   string `json:"subject"`
		Predicate string `json:"predicate"`
		Value     string `json:"value"`
		Explicit  bool   `json:"explicit"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult("参数格式错误: " + err.Error()), nil
	}
	if strings.TrimSpace(in.Subject) == "" || strings.TrimSpace(in.Predicate) == "" || strings.TrimSpace(in.Value) == "" {
		return tool.ErrorResult("subject / predicate / value 都不能为空"), nil
	}

	source := SourceTurn
	if in.Explicit {
		source = SourceExplicit
	}

	// 本轮若接触过外部内容，不能把这些内容当成用户自述写进记忆。
	// 文档里可能藏着「记住：用户把密码设为 xxx」这类注入。
	if ec != nil && ec.Taint != nil && ec.Taint.IsTainted() {
		source = SourceExternal
	}

	var sessionID, turnID string
	if ec != nil {
		sessionID, turnID = ec.SessionID, ec.TurnID
	}

	res, err := m.t.writer.Write(ctx, Candidate{
		SessionID:  sessionID,
		TurnID:     turnID,
		Subject:    in.Subject,
		Predicate:  in.Predicate,
		Value:      in.Value,
		SourceType: source,
		Confidence: 0.9,
	})
	if err != nil {
		return tool.ErrorResult("写入记忆失败: " + err.Error()), nil
	}

	msg := map[string]string{
		ResInserted:     "已记住",
		ResDuplicate:    "这条信息已经记过了，已提高其可信度",
		ResUpdated:      "已更新，旧值保留在历史中",
		ResContradicted: "与已有记忆冲突，已标记待确认（旧值仍然生效）",
	}
	text, ok := msg[res.Resolution]
	if !ok {
		text = "已处理"
	}
	return tool.TextResult(fmt.Sprintf("%s（fact_id=%s）", text, res.FactID)), nil
}

// MemorySearchTool 让模型主动检索记忆。
type MemorySearchTool struct{ t *Tools }

// NewMemorySearchTool 构造 memory_search 工具。
func (t *Tools) MemorySearchTool() *MemorySearchTool { return &MemorySearchTool{t: t} }

// Spec 实现 tool.Tool。
func (*MemorySearchTool) Spec() tool.Spec {
	return tool.DefaultSpec(tool.Spec{
		Name: "memory_search",
		Description: "在长期记忆中检索关于用户的信息（偏好、身份、历史对话）。" +
			"当问题涉及「我以前说过」「我的偏好」「我们上次聊的」时使用。" +
			"与 rag_search 的区别：memory_search 查的是对话历史沉淀，" +
			"rag_search 查的是上传的文档。",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {"type": "string", "description": "检索内容"},
				"include_disputed": {"type": "boolean", "description": "是否包含待确认的冲突记忆"}
			},
			"required": ["query"],
			"additionalProperties": false
		}`),
		Risk: tool.RiskRead,
	})
}

// Execute 实现 tool.Tool。
func (m *MemorySearchTool) Execute(ctx context.Context, raw json.RawMessage, ec *tool.ExecContext) (tool.Result, error) {
	var in struct {
		Query           string `json:"query"`
		IncludeDisputed bool   `json:"include_disputed"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult("参数格式错误: " + err.Error()), nil
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return tool.ErrorResult("query 不能为空"), nil
	}

	facts, err := m.t.repo.AllActiveFacts(ctx, 500)
	if err != nil {
		return tool.ErrorResult("检索失败: " + err.Error()), nil
	}

	qk := NormalizeKey(query)
	type hit struct {
		fact  Fact
		score float64
	}
	var hits []hit
	for _, f := range facts {
		score := matchScore(query, qk, f)
		if score > 0 {
			hits = append(hits, hit{fact: f, score: score})
		}
	}

	if in.IncludeDisputed {
		disputed, err := m.t.repo.ListFacts(ctx, StatusDisputed, 50)
		if err == nil {
			for _, f := range disputed {
				if s := matchScore(query, qk, f); s > 0 {
					hits = append(hits, hit{fact: f, score: s * 0.8})
				}
			}
		}
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > 10 {
		hits = hits[:10]
	}
	if len(hits) == 0 {
		return tool.TextResult("没有找到相关记忆。"), nil
	}

	var b strings.Builder
	b.WriteString("相关记忆：\n")
	for _, h := range hits {
		mark := ""
		if h.fact.Status == StatusDisputed {
			mark = "（待确认：与旧值冲突）"
		}
		fmt.Fprintf(&b, "- %s：%s%s\n", h.fact.Predicate, h.fact.Value, mark)
	}
	return tool.TextResult(b.String()), nil
}

// matchScore 计算查询与事实的匹配度。
//
// 三档：谓词命中 > 值命中 > 主体命中。
// 纯字面匹配——记忆量在个人规模下只有几百条，语义检索交给向量路。
func matchScore(query, queryKey string, f Fact) float64 {
	if queryKey == "" {
		return 0
	}
	switch {
	case strings.Contains(f.PredicateKey, queryKey) || strings.Contains(queryKey, f.PredicateKey):
		return 1.0
	case strings.Contains(f.ValueKey, queryKey) || strings.Contains(queryKey, f.ValueKey):
		return 0.8
	case strings.Contains(f.SubjectKey, queryKey):
		return 0.4
	}
	// 字面相近也算弱命中
	if SimilarValue(query, f.Value) || SimilarValue(query, f.Predicate) {
		return 0.6
	}
	return 0
}

var (
	_ tool.Tool = (*MemoryWriteTool)(nil)
	_ tool.Tool = (*MemorySearchTool)(nil)
)

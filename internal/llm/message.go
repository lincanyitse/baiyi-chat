// Package llm 是全系统的契约根：统一消息模型、流式事件、能力清单、
// 错误归一化与 Provider 接口。
//
// 本包只依赖标准库，零副作用，任何包都可安全依赖它。
// 所有 Provider 适配器都向这里的模型靠拢，而不是相反。
package llm

import (
	"encoding/json"
	"strings"
	"time"
)

// Role 是消息角色。注意：tool 结果不是独立角色，而是 user 消息内的 Block，
// 这样 Anthropic 与 OpenAI 的差异可以完全关在适配层。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Trust 表示内容来源的可信度，用于提示词注入防护。
type Trust string

const (
	TrustSystem   Trust = "system"
	TrustUser     Trust = "user"
	TrustExternal Trust = "external" // 网页、RAG、MCP 工具描述等不可信来源
)

// Message 是一条对话消息。
type Message struct {
	ID        string      `json:"id,omitempty"`
	Role      Role        `json:"role"`
	Content   []Block     `json:"content"`
	CreatedAt time.Time   `json:"created_at,omitzero"`
	Meta      MessageMeta `json:"meta,omitzero"`
}

// MessageMeta 承载非内容元数据。
type MessageMeta struct {
	ParentID   string   `json:"parent_id,omitempty"`
	Pinned     bool     `json:"pinned,omitempty"`
	Trust      Trust    `json:"trust,omitempty"`
	SourceRefs []string `json:"source_refs,omitempty"`
	Compacted  bool     `json:"compacted,omitempty"`
	TokenCount int      `json:"token_count,omitempty"`
}

// BlockType 是内容块的类型标识。
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockImage      BlockType = "image"
	BlockFile       BlockType = "file"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
	BlockThinking   BlockType = "thinking"
	BlockCitation   BlockType = "citation"
	BlockAudio      BlockType = "audio"
)

// Block 是消息的最小组成单元。
type Block interface {
	BlockType() BlockType
}

// TextBlock 是纯文本。
type TextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (TextBlock) BlockType() BlockType { return BlockText }

// ImageSource 描述图片来源：URL 或 Base64 数据。
type ImageSource struct {
	URL       string `json:"url,omitempty"`
	Data      []byte `json:"data,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

// ImageBlock 是一张图片。Detail 对应 OpenAI 的 detail 档位（low/high/auto）。
type ImageBlock struct {
	Type   string      `json:"type"`
	Source ImageSource `json:"source"`
	Detail string      `json:"detail,omitempty"`
}

func (ImageBlock) BlockType() BlockType { return BlockImage }

// FileBlock 是文档附件。
type FileBlock struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	MediaType string `json:"media_type,omitempty"`
	Data      []byte `json:"data,omitempty"`
	URI       string `json:"uri,omitempty"`
}

func (FileBlock) BlockType() BlockType { return BlockFile }

// ToolUseBlock 是模型发起的一次工具调用。Input 是完整 JSON（适配层保证）。
type ToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (ToolUseBlock) BlockType() BlockType { return BlockToolUse }

// ToolResultBlock 是工具执行结果，回填给模型。
type ToolResultBlock struct {
	Type      string  `json:"type"`
	ToolUseID string  `json:"tool_use_id"`
	Content   []Block `json:"content"`
	IsError   bool    `json:"is_error,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
	Handle    string  `json:"handle,omitempty"`
}

func (ToolResultBlock) BlockType() BlockType { return BlockToolResult }

// ThinkingBlock 是推理过程。Signature 必须按 Provider 规则处理：
// Anthropic 要求原样带回，DeepSeek 系反而不能回传。
type ThinkingBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

func (ThinkingBlock) BlockType() BlockType { return BlockThinking }

// CitationBlock 是 RAG 引用。
//
// 字段足以让 UI 从「回答里的 [3]」跳到原文的确切位置：
// DocID + ChunkID 定位片段，Start/End 是片段内字符偏移，
// HeadingPath 与 Page 用于在没有原文渲染能力时给人看。
type CitationBlock struct {
	Type        string `json:"type"`
	Index       int    `json:"index"` // 回答正文中的编号，从 1 开始
	DocID       string `json:"doc_id"`
	ChunkID     string `json:"chunk_id"`
	Start       int    `json:"start"`
	End         int    `json:"end"`
	Title       string `json:"title,omitempty"`
	HeadingPath string `json:"heading_path,omitempty"`
	Page        int    `json:"page,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
	URI         string `json:"uri,omitempty"`
}

func (CitationBlock) BlockType() BlockType { return BlockCitation }

// AudioBlock 是音频。
type AudioBlock struct {
	Type       string `json:"type"`
	MediaType  string `json:"media_type"`
	Data       []byte `json:"data,omitempty"`
	DurationMS int    `json:"duration_ms,omitempty"`
}

func (AudioBlock) BlockType() BlockType { return BlockAudio }

// 构造函数保证 Type 字段始终正确，调用方无需手写。

// NewTextBlock 构造文本块。
func NewTextBlock(s string) TextBlock { return TextBlock{Type: string(BlockText), Text: s} }

// NewImageBlock 构造图片块。
func NewImageBlock(src ImageSource, detail string) ImageBlock {
	return ImageBlock{Type: string(BlockImage), Source: src, Detail: detail}
}

// NewToolUseBlock 构造工具调用块。
func NewToolUseBlock(id, name string, input json.RawMessage) ToolUseBlock {
	return ToolUseBlock{Type: string(BlockToolUse), ID: id, Name: name, Input: input}
}

// NewToolResultBlock 构造工具结果块。
func NewToolResultBlock(id string, content []Block, isErr bool) ToolResultBlock {
	return ToolResultBlock{Type: string(BlockToolResult), ToolUseID: id, Content: content, IsError: isErr}
}

// NewThinkingBlock 构造思考块。
func NewThinkingBlock(text, sig string) ThinkingBlock {
	return ThinkingBlock{Type: string(BlockThinking), Text: text, Signature: sig}
}

// NewText 构造一条只含文本的消息，方便调用。
func NewText(role Role, text string) Message {
	return Message{Role: role, Content: []Block{NewTextBlock(text)}}
}

// TextOf 提取消息中所有文本块的拼接结果。
func TextOf(m Message) string { return TextOfBlocks(m.Content) }

// TextOfBlocks 提取一组内容块中所有文本的拼接结果。
func TextOfBlocks(blocks []Block) string {
	var b strings.Builder
	for _, blk := range blocks {
		if t, ok := blk.(TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

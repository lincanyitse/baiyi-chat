// Package rag 提供文档接入、切分、混合检索与引用。
//
// 依赖约束：只依赖 llm 与 embed。memory 与本包永不互相依赖——
// 两者都需要嵌入时各自消费 internal/embed。
package rag

import (
	"errors"
	"time"
)

// Doc 是一份已解析的文档。
type Doc struct {
	// Source 是原始来源标识：本地路径、URL 或用户给定的名字。
	Source string
	// Title 是展示用标题，留空时由 Source 的基名推导。
	Title string
	// MIME 是识别出的类型。
	MIME string
	// SHA256 是原始内容指纹，用于去重。
	SHA256 string
	// SizeBytes 是原始字节数。
	SizeBytes int64
	// Sections 是按文档顺序排列的结构化块。
	Sections []Section
}

// CharCount 返回解析后的总字符数。
func (d Doc) CharCount() int {
	n := 0
	for _, s := range d.Sections {
		n += len([]rune(s.Text))
	}
	return n
}

// SectionKind 是块的类型，决定切分策略。
type SectionKind string

const (
	// KindHeading 是标题行。它本身不入库，只用于维护 heading_path。
	KindHeading SectionKind = "heading"
	// KindPara 是普通段落，可自由切分。
	KindPara SectionKind = "para"
	// KindTable 是表格，视为原子块。
	KindTable SectionKind = "table"
	// KindCode 是代码块，视为原子块。
	KindCode SectionKind = "code"
)

// Section 是解析产出的一个结构单元。
type Section struct {
	// HeadingPath 是祖先标题用 " / " 连接，例如 "运维手册 / 部署 / 容器"。
	HeadingPath string
	Kind        SectionKind
	Text        string
	// Level 是标题级别（1-6），仅 KindHeading 有意义。
	Level int
	// Page 是页码，从 1 开始；纯文本为 0。
	Page int
	// CharOffset 是 Text 在原文解析文本中的起始字符位置。
	//
	// 用于计算引用的可定位偏移。解析器能精确给出时填真实值；
	// 结构被重排过的（如 HTML 去掉导航后 DOM 拼接）填 0，
	// 此时引用只能定位到 chunk 本身，不能回跳原文行号。
	CharOffset int
}

// Chunk 是切分后的检索单元。
type Chunk struct {
	ID          string
	DocumentID  string
	Ordinal     int
	HeadingPath string
	Kind        SectionKind
	Text        string
	// CharStart / CharEnd 是相对文档解析文本的字符偏移，用于引用定位。
	CharStart int
	CharEnd   int
	Page      int
	TokenEst  int
}

// EmbedText 返回用于嵌入的文本：拼接标题路径以保留上下文。
//
// 只把正文存进 chunks.text：展示与引用需要的是原文，
// 而「这段属于哪一章」在嵌入时需要，展示时已有独立字段。
func (c Chunk) EmbedText() string {
	if c.HeadingPath == "" {
		return c.Text
	}
	return c.HeadingPath + "\n" + c.Text
}

// Citation 是一条可定位的引用。
type Citation struct {
	ChunkID     string `json:"chunk_id"`
	DocumentID  string `json:"document_id"`
	Title       string `json:"title"`
	HeadingPath string `json:"heading_path,omitempty"`
	Page        int    `json:"page,omitempty"`
	CharStart   int    `json:"char_start"`
	CharEnd     int    `json:"char_end"`
	Snippet     string `json:"snippet"`
	// Score 是 RRF 融合分或重排分，只用于排序，不是「相关性百分比」。
	//
	// RRF 分的量级由 1/(60+rank) 决定，双路都排第一也只有 0.0328。
	// 把它展示给用户会误导成「相关性 3%」，所以另有 VectorScore。
	Score float32 `json:"score"`
	// VectorScore 是查询与片段向量的余弦相似度（0-1，越大越相关）。
	//
	// 只有向量召回命中时才有意义；-1 表示未知（未配置嵌入或只走了关键词）。
	// 展示给用户看的是这个，不是 Score。
	VectorScore float32 `json:"vector_score"`
	// Source 是原始来源，便于用户回溯。
	Source string `json:"source,omitempty"`
}

// DocumentStatus 是文档的索引进度。
type DocumentStatus string

const (
	DocPending  DocumentStatus = "pending"
	DocIndexing DocumentStatus = "indexing"
	DocReady    DocumentStatus = "ready"
	DocFailed   DocumentStatus = "failed"
)

// Document 是文档的持久化视图。
type Document struct {
	ID         string         `json:"id"`
	Source     string         `json:"source"`
	Title      string         `json:"title"`
	MIME       string         `json:"mime"`
	SHA256     string         `json:"sha256"`
	SizeBytes  int64          `json:"size_bytes"`
	CharCount  int            `json:"char_count"`
	Status     DocumentStatus `json:"status"`
	Error      string         `json:"error,omitempty"`
	EmbedModel string         `json:"embed_model,omitempty"`
	ChunkCount int            `json:"chunk_count"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// SearchResult 是一次检索的完整结果。
type SearchResult struct {
	Chunks []Citation `json:"chunks"`
	// Degraded 为真表示向量召回不可用，只用了关键词。
	Degraded bool   `json:"degraded"`
	Reason   string `json:"reason,omitempty"`
	// Reranked 为真表示结果经过重排。
	Reranked bool `json:"reranked"`
}

// 常见错误。
var (
	// ErrUnsupported 表示文件类型不在支持范围内。
	ErrUnsupported = errors.New("rag: 不支持的文件类型")
	// ErrEmptyDocument 表示解析后没有可索引内容。
	ErrEmptyDocument = errors.New("rag: 文档没有可索引内容")
	// ErrNotFound 表示文档或片段不存在。
	ErrNotFound = errors.New("rag: 未找到")
)

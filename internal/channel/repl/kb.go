package repl

import (
	"context"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/rag"
)

func (r *REPL) handleKB(ctx context.Context, arg string) {
	if r.kb == nil {
		r.printf("知识库未启用（启动时初始化失败，详见日志）。\n")
		return
	}

	sub, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	rest = strings.TrimSpace(rest)

	switch sub {
	case "", "list":
		r.kbList(ctx)
	case "add":
		if rest == "" {
			r.printf("用法: /kb add <文件路径>\n")
			return
		}
		doc, err := r.kb.IngestFile(ctx, rest)
		if err != nil {
			r.printf("接入失败: %v\n", err)
			return
		}
		r.printf("已接入：%s（%d 字，状态 %s）\n", doc.Title, doc.CharCount, doc.Status)
		if !r.kb.EmbedActive() {
			r.printf("提示：未配置嵌入服务，检索将只走关键词。\n")
		}
	case "search":
		if rest == "" {
			r.printf("用法: /kb search <关键词>\n")
			return
		}
		res, err := r.kb.Search(ctx, rest, 8)
		if err != nil {
			r.printf("检索失败: %v\n", err)
			return
		}
		r.printf("%s\n", rag.CitationBlock(res))
		if res.Degraded {
			r.printf("（降级：%s）\n", res.Reason)
		}
	case "rm":
		if rest == "" {
			r.printf("用法: /kb rm <文档短ID>\n")
			return
		}
		r.kbRemove(ctx, rest)
	case "rebuild":
		if !r.kb.EmbedActive() {
			r.printf("未配置嵌入服务，无法重建向量。\n")
			return
		}
		r.printf("开始重建向量，模型 %s。\n", r.kb.EmbedModel())
		rc, err := r.kb.RebuildEmbeddings(ctx, r.kb.EmbedModel())
		if err != nil {
			r.printf("重建失败: %v\n", err)
			return
		}
		r.printf("完成：处理 %d 条，失败 %d 条。\n", rc.Done, rc.Failed)
	case "stats":
		r.kbStats(ctx)
	default:
		r.printf("未知子命令 %q。可用：list / add / search / rm / rebuild / stats\n", sub)
	}
}

func (r *REPL) kbStats(ctx context.Context) {
	docs, err := r.kb.ListDocuments(ctx, 10000)
	if err != nil {
		r.printf("读取失败: %v\n", err)
		return
	}

	var ready, failed, pending int
	var chars int
	for _, d := range docs {
		chars += d.CharCount
		switch d.Status {
		case rag.DocReady:
			ready++
		case rag.DocFailed:
			failed++
		default:
			pending++
		}
	}

	model := "未配置（仅关键词检索）"
	if r.kb.EmbedActive() {
		model = r.kb.EmbedModel()
	}
	r.printf("文档：%d 份（就绪 %d，失败 %d，处理中 %d）\n", len(docs), ready, failed, pending)
	r.printf("总字符：%d\n", chars)
	r.printf("嵌入模型：%s\n", model)

	// 检索耗时实测：比任何静态数字都更能反映真实体验
	start := time.Now()
	if _, err := r.kb.Search(ctx, "测试查询", 3); err == nil {
		r.printf("检索耗时：%v\n", time.Since(start).Round(time.Millisecond))
	}
}

func (r *REPL) kbList(ctx context.Context) {
	docs, err := r.kb.ListDocuments(ctx, 50)
	if err != nil {
		r.printf("读取失败: %v\n", err)
		return
	}
	if len(docs) == 0 {
		r.printf("知识库为空。用 /kb add <路径> 添加文档。\n")
		return
	}

	status := "未配置嵌入"
	if r.kb.EmbedActive() {
		status = r.kb.EmbedModel()
	}
	r.printf("嵌入：%s\n\n", status)
	for _, d := range docs {
		model := d.EmbedModel
		if model == "" {
			model = "无向量"
		}
		r.printf("  %s  %-24s %6d 字  %-6s  %s\n",
			shortID(d.ID), truncateBytes(d.Title, 24), d.CharCount, d.Status, model)
	}
}

func (r *REPL) kbRemove(ctx context.Context, short string) {
	docs, err := r.kb.ListDocuments(ctx, 200)
	if err != nil {
		r.printf("读取失败: %v\n", err)
		return
	}

	var matched []rag.Document
	for _, d := range docs {
		if matchesID(d.ID, short) {
			matched = append(matched, d)
		}
	}

	switch len(matched) {
	case 0:
		r.printf("未找到文档: %s\n", short)
	case 1:
		if err := r.kb.DeleteDocument(ctx, matched[0].ID); err != nil {
			r.printf("删除失败: %v\n", err)
			return
		}
		r.printf("已删除：%s\n", matched[0].Title)
	default:
		r.printf("匹配到 %d 个文档，请提供更精确的 ID：\n", len(matched))
		for _, d := range matched {
			r.printf("  %s  %s\n", shortID(d.ID), d.Title)
		}
	}
}

func truncateBytes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

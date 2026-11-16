// Package export 把会话导出成人能读的 Markdown 或程序能读的 JSONL。
//
// 两种格式各有明确用途：Markdown 用于阅读与分享，JSONL 保留完整结构
// （含 parent_id 与原始内容块），是将来「重新导入」的基础。
package export

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

// Doc 是一次导出的完整输入。
type Doc struct {
	Session  session.Session
	Messages []session.StoredMessage
}

// Markdown 把会话渲染成 Markdown。
//
// 主链（沿 parent_id 回溯到根的最长链）放正文，被丢弃的分支折叠进
// <details>，附在它分叉出来的那条消息之后。这样既不丢内容，
// 也不让正文变成一团乱麻。
func Markdown(doc Doc) string {
	var b strings.Builder

	title := doc.Session.Title
	if title == "" {
		title = "未命名会话"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)

	fmt.Fprintf(&b, "> 导出时间：%s · %d 条消息",
		time.Now().Format("2006-01-02 15:04"), len(doc.Messages))
	if doc.Session.ModelRef != "" {
		fmt.Fprintf(&b, " · 模型：`%s`", doc.Session.ModelRef)
	}
	b.WriteString("\n\n")

	main := mainChain(doc.Messages)
	byParent := childrenIndex(doc.Messages)

	for _, m := range main {
		writeMessage(&b, m)
		// 分叉：这条消息下面有多个子节点时，非主链的折叠起来
		writeBranches(&b, byParent[m.ID], main)
	}
	return b.String()
}

// mainChain 返回从根到叶的主链。
//
// 主链的选择规则：每个分叉点选子节点里 seq 最大的——那是用户最后
// 一次编辑/重生成留下的结果，也就是他实际看到的对话。
func mainChain(msgs []session.StoredMessage) []session.StoredMessage {
	if len(msgs) == 0 {
		return nil
	}

	byID := indexByID(msgs)
	children := childrenIndex(msgs)

	// 找根：parent_id 为空，或父节点不在本次导出范围内
	var roots []session.StoredMessage
	for _, m := range msgs {
		if m.ParentID == "" {
			roots = append(roots, m)
			continue
		}
		if _, ok := byID[m.ParentID]; !ok {
			roots = append(roots, m)
		}
	}
	if len(roots) == 0 {
		return nil
	}

	// 从 seq 最大的根出发，一路选 seq 最大的子节点
	sort.Slice(roots, func(i, j int) bool { return roots[i].Seq < roots[j].Seq })
	cur := roots[len(roots)-1]

	var chain []session.StoredMessage
	for {
		chain = append(chain, cur)
		kids := children[cur.ID]
		if len(kids) == 0 {
			break
		}
		sort.Slice(kids, func(i, j int) bool { return kids[i].Seq < kids[j].Seq })
		cur = kids[len(kids)-1]
	}
	return chain
}

func indexByID(msgs []session.StoredMessage) map[string]session.StoredMessage {
	out := make(map[string]session.StoredMessage, len(msgs))
	for _, m := range msgs {
		out[m.ID] = m
	}
	return out
}

func childrenIndex(msgs []session.StoredMessage) map[string][]session.StoredMessage {
	out := make(map[string][]session.StoredMessage)
	for _, m := range msgs {
		if m.ParentID != "" {
			out[m.ParentID] = append(out[m.ParentID], m)
		}
	}
	return out
}

// writeMessage 渲染一条消息。
func writeMessage(b *strings.Builder, m session.StoredMessage) {
	role := roleLabel(m.Role)
	fmt.Fprintf(b, "## %s\n\n", role)

	for _, blk := range m.Content {
		switch v := blk.(type) {
		case llm.TextBlock:
			if text := strings.TrimSpace(v.Text); text != "" {
				b.WriteString(text)
				b.WriteString("\n\n")
			}
		case *llm.TextBlock:
			if text := strings.TrimSpace(v.Text); text != "" {
				b.WriteString(text)
				b.WriteString("\n\n")
			}
		case llm.ToolUseBlock:
			writeToolUse(b, v)
		case *llm.ToolUseBlock:
			writeToolUse(b, *v)
		case llm.ToolResultBlock:
			writeToolResult(b, v)
		case *llm.ToolResultBlock:
			writeToolResult(b, *v)
		case llm.ThinkingBlock:
			writeThinking(b, v.Text)
		case *llm.ThinkingBlock:
			writeThinking(b, v.Text)
		case llm.ImageBlock, *llm.ImageBlock:
			b.WriteString("*（图片，未在导出中包含）*\n\n")
		case llm.FileBlock, *llm.FileBlock:
			b.WriteString("*（附件，未在导出中包含）*\n\n")
		}
	}
}

func writeToolUse(b *strings.Builder, v llm.ToolUseBlock) {
	fmt.Fprintf(b, "<details>\n<summary>调用工具：%s</summary>\n\n", v.Name)
	if len(v.Input) > 0 {
		fmt.Fprintf(b, "```json\n%s\n```\n\n", prettyJSON(v.Input))
	}
	b.WriteString("</details>\n\n")
}

func writeToolResult(b *strings.Builder, v llm.ToolResultBlock) {
	fmt.Fprintf(b, "<details>\n<summary>工具结果%s</summary>\n\n",
		errSuffix(v.IsError))
	for _, blk := range v.Content {
		if t, ok := blk.(llm.TextBlock); ok {
			fmt.Fprintf(b, "```\n%s\n```\n\n", truncate(t.Text, 2000))
		} else if t, ok := blk.(*llm.TextBlock); ok {
			fmt.Fprintf(b, "```\n%s\n```\n\n", truncate(t.Text, 2000))
		}
	}
	b.WriteString("</details>\n\n")
}

func writeThinking(b *strings.Builder, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	b.WriteString("<details>\n<summary>思考过程</summary>\n\n")
	b.WriteString(text)
	b.WriteString("\n\n</details>\n\n")
}

// writeBranches 把非主链的分支折叠导出。
func writeBranches(b *strings.Builder, kids []session.StoredMessage, main []session.StoredMessage) {

	if len(kids) <= 1 {
		return
	}
	onMain := make(map[string]bool, len(main))
	for _, m := range main {
		onMain[m.ID] = true
	}

	var off []session.StoredMessage
	for _, k := range kids {
		if !onMain[k.ID] {
			off = append(off, k)
		}
	}
	if len(off) == 0 {
		return
	}

	sort.Slice(off, func(i, j int) bool { return off[i].Seq < off[j].Seq })
	b.WriteString("<details>\n<summary>未采用的分支</summary>\n\n")
	for _, m := range off {
		writeMessage(b, m)
	}
	b.WriteString("</details>\n\n")
}

func roleLabel(r llm.Role) string {
	switch r {
	case llm.RoleUser:
		return "用户"
	case llm.RoleAssistant:
		return "助手"
	case llm.RoleSystem:
		return "系统"
	default:
		return string(r)
	}
}

func errSuffix(isErr bool) string {
	if isErr {
		return "（失败）"
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…（已截断）"
}

package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// LoadTool 是暴露给模型的 load_skill 工具。
//
// 关键设计：技能正文以 tool_result 的形式进入上下文，而不是写进 system。
// 写进 system 会让「工具与技能目录」这一段每次都变化，整段前缀缓存失效——
// 渐进披露能成立的前提就是正文不污染稳定前缀。
type LoadTool struct {
	catalog *Catalog
}

// NewLoadTool 构造工具。
func NewLoadTool(c *Catalog) *LoadTool { return &LoadTool{catalog: c} }

// Spec 实现 tool.Tool。
func (t *LoadTool) Spec() tool.Spec {
	return tool.DefaultSpec(tool.Spec{
		Name: "load_skill",
		Description: "加载一项技能的完整流程说明。当任务与技能目录中某项技能匹配时调用。" +
			"技能正文包含具体步骤、规则与示例，加载后按其中流程执行。",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "技能名称，来自技能目录"
				},
				"reference": {
					"type": "string",
					"description": "可选：要读取的参考资料路径，如 references/format.md"
				}
			},
			"required": ["name"],
			"additionalProperties": false
		}`),
		Risk:    tool.RiskRead,
		Timeout: 10 * time.Second,
	})
}

// Execute 实现 tool.Tool。
func (t *LoadTool) Execute(ctx context.Context, in json.RawMessage, ec *tool.ExecContext) (tool.Result, error) {
	var args struct {
		Name      string `json:"name"`
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(in, &args); err != nil {
		return tool.ErrorResult("参数解析失败: " + err.Error()), nil
	}

	s, ok := t.catalog.Get(args.Name)
	if !ok {
		return tool.ErrorResult(fmt.Sprintf(
			"技能 %q 不存在。可用技能：%s", args.Name, t.availableNames())), nil
	}

	// L2：读取参考资料
	if args.Reference != "" {
		content, err := s.Reference(args.Reference)
		if err != nil {
			return tool.ErrorResult(err.Error()), nil
		}
		return tool.TextResult(fmt.Sprintf(
			"# 技能 %s 的参考资料 %s\n\n%s", s.Spec.Name, args.Reference, content)), nil
	}

	// L1：技能正文
	var b strings.Builder
	fmt.Fprintf(&b, "# 技能：%s\n", s.Spec.Name)
	if s.Spec.Version != "" {
		fmt.Fprintf(&b, "版本：%s\n", s.Spec.Version)
	}
	b.WriteString("\n")
	b.WriteString(s.Body)

	if len(s.References) > 0 {
		b.WriteString("\n\n## 参考资料（按需读取）\n")
		for _, r := range s.References {
			fmt.Fprintf(&b, "- %s\n", r)
		}
		b.WriteString("\n需要时用 load_skill 的 reference 参数读取具体文件。\n")
	}

	return tool.TextResult(b.String()), nil
}

// availableNames 返回当前可用技能名，供错误提示。
func (t *LoadTool) availableNames() string {
	all := t.catalog.All()
	if len(all) == 0 {
		return "(无)"
	}
	names := make([]string, 0, len(all))
	for _, s := range all {
		names = append(names, s.Spec.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

var _ tool.Tool = (*LoadTool)(nil)

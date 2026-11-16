package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// ToolOutputRead 按句柄读取被截断的完整工具输出。
//
// 它是上下文预算降级链的配套：大结果被替换成句柄后，
// 模型需要时能自己取回原文。
type ToolOutputRead struct{}

// NewToolOutputRead 构造工具。
func NewToolOutputRead() *ToolOutputRead { return &ToolOutputRead{} }

// Spec 实现 tool.Tool。
func (t *ToolOutputRead) Spec() tool.Spec {
	return tool.DefaultSpec(tool.Spec{
		Name:        "tool_output_read",
		Description: "读取之前被截断的工具输出。当工具结果里出现「句柄 xxx，可用 tool_output_read 取回」时，用该句柄读取完整内容。",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"handle": {
					"type": "string",
					"description": "结果中给出的句柄"
				},
				"offset": {
					"type": "integer",
					"description": "字节偏移，默认 0"
				},
				"limit": {
					"type": "integer",
					"description": "最多返回字节数，默认 16000"
				}
			},
			"required": ["handle"],
			"additionalProperties": false
		}`),
		Risk:    tool.RiskRead,
		Timeout: 10 * time.Second,
	})
}

// Execute 实现 tool.Tool。
func (t *ToolOutputRead) Execute(ctx context.Context, in json.RawMessage, ec *tool.ExecContext) (tool.Result, error) {
	var args struct {
		Handle string `json:"handle"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(in, &args); err != nil {
		return tool.ErrorResult("参数解析失败: " + err.Error()), nil
	}
	if args.Handle == "" {
		return tool.ErrorResult("handle 不能为空"), nil
	}
	if args.Limit <= 0 {
		args.Limit = 16000
	}
	if ec.Outputs == nil {
		return tool.ErrorResult("当前环境未启用工具输出存储"), nil
	}

	content, total, err := ec.Outputs.Load(ctx, args.Handle, args.Offset, args.Limit)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}

	if content == "" {
		return tool.TextResult(fmt.Sprintf("句柄 %s 已到末尾（总长 %d 字节）", args.Handle, total)), nil
	}

	end := args.Offset + len(content)
	header := fmt.Sprintf("[句柄 %s，第 %d-%d 字节，共 %d 字节]\n",
		args.Handle, args.Offset, end, total)
	return tool.TextResult(header + content), nil
}

var _ tool.Tool = (*ToolOutputRead)(nil)

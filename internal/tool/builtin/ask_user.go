package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// AskUser 让模型向用户提出结构化澄清问题。
//
// 这个工具被严重低估：与其让模型在信息不足时瞎猜，
// 不如让它主动停下来问一句。它走审批通道（AwaitUser），
// 不阻塞 goroutine，用户下次输入即恢复。
type AskUser struct{}

// NewAskUser 构造工具。
func NewAskUser() *AskUser { return &AskUser{} }

// Spec 实现 tool.Tool。
func (t *AskUser) Spec() tool.Spec {
	return tool.DefaultSpec(tool.Spec{
		Name: "ask_user",
		Description: "向用户提问以澄清需求。仅在信息不足以继续、且猜测可能造成严重后果时使用。" +
			"不要在能从上下文推断时使用，也不要用它代替正常回答。",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"question": {
					"type": "string",
					"description": "要问用户的问题，应具体明确"
				},
				"options": {
					"type": "array",
					"items": {"type": "string"},
					"description": "可选的候选项，提供时用户可直接选择"
				}
			},
			"required": ["question"],
			"additionalProperties": false
		}`),
		Risk:    tool.RiskRead,
		Timeout: 10 * time.Minute, // 等待用户回答，给足时间
	})
}

// Execute 实现 tool.Tool。
//
// 这里的实现假定调用方已经把用户回答通过 ExecContext 注入；
// 真正的挂起-恢复由 agent 与渠道协作完成（见 ApprovalRequest）。
func (t *AskUser) Execute(ctx context.Context, in json.RawMessage, ec *tool.ExecContext) (tool.Result, error) {
	var args struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal(in, &args); err != nil {
		return tool.ErrorResult("参数解析失败: " + err.Error()), nil
	}
	if args.Question == "" {
		return tool.ErrorResult("question 不能为空"), nil
	}

	if ec == nil || ec.Approver == nil {
		// 无人值守：明确告知模型无法询问，让它自行决定
		return tool.ErrorResult(
			"当前没有可交互的用户，无法提问。请基于已知信息作出最合理的假设并说明你的假设。",
		), nil
	}

	// 走审批通道：把问题包装成一次「审批」，选项作为附带信息。
	decision, err := ec.Approver.Request(ctx, tool.ApprovalRequest{
		TurnID:    ec.TurnID,
		SessionID: ec.SessionID,
		Calls: []tool.ApprovalCall{{
			CallID:  ec.CallID,
			Name:    "ask_user",
			Risk:    tool.RiskRead,
			Summary: args.Question,
		}},
		ExpiresAt: ec.Now().Add(10 * time.Minute),
	})
	if err != nil {
		return tool.ErrorResult("等待用户回答失败: " + err.Error()), nil
	}

	answer := decision.Reason
	if answer == "" {
		return tool.ErrorResult("用户未作答"), nil
	}
	return tool.TextResult(fmt.Sprintf("用户回答：%s", answer)), nil
}

var _ tool.Tool = (*AskUser)(nil)

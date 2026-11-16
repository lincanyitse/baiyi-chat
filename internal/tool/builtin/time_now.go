// Package builtin 提供首期内置工具。
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// TimeNow 返回当前时间。
//
// 存在的意义不只是报时：有了它，系统提示里就不必写死当前时间，
// 稳定前缀得以保持不变，前缀缓存才不会每轮失效。
type TimeNow struct{}

// NewTimeNow 构造工具。
func NewTimeNow() *TimeNow { return &TimeNow{} }

// Spec 实现 tool.Tool。
func (t *TimeNow) Spec() tool.Spec {
	return tool.DefaultSpec(tool.Spec{
		Name:        "time_now",
		Description: "获取当前日期与时间。当需要知道「今天几号」「现在几点」「今天星期几」时必须调用，不要凭记忆回答。",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"timezone": {
					"type": "string",
					"description": "IANA 时区名，如 Asia/Shanghai。留空则用本地时区。"
				}
			},
			"additionalProperties": false
		}`),
		Risk:    tool.RiskRead,
		Timeout: 5 * time.Second,
	})
}

// Execute 实现 tool.Tool。
func (t *TimeNow) Execute(ctx context.Context, in json.RawMessage, ec *tool.ExecContext) (tool.Result, error) {
	var args struct {
		Timezone string `json:"timezone"`
	}
	if len(in) > 0 {
		if err := json.Unmarshal(in, &args); err != nil {
			return tool.ErrorResult("参数解析失败: " + err.Error()), nil
		}
	}

	now := ec.Now()
	loc := now.Location()
	if args.Timezone != "" {
		l, err := time.LoadLocation(args.Timezone)
		if err != nil {
			return tool.ErrorResult(fmt.Sprintf("未知时区 %q", args.Timezone)), nil
		}
		loc = l
		now = now.In(l)
	}

	const layout = "2006-01-02 15:04:05 Monday"
	return tool.TextResult(fmt.Sprintf(
		"%s（时区 %s）", now.Format(layout), loc.String())), nil
}

var _ tool.Tool = (*TimeNow)(nil)
var _ = llm.BlockText

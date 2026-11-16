// Package sse 实现 Server-Sent Events 的流式分帧解析。
// 两家 Provider 的流式响应都基于 SSE，事件格式不同但分帧规则一致。
package sse

import (
	"bufio"
	"io"
	"strings"
)

// Event 是一个已分帧的 SSE 事件。
type Event struct {
	Name string // event: 字段，可能为空
	Data string // data: 字段（多行会以 \n 连接）
}

// Scanner 逐事件读取 SSE 流。
type Scanner struct {
	sc *bufio.Scanner
}

// NewScanner 构造 SSE 扫描器。单行上限 1MB，足以容纳任何工具的完整参数。
func NewScanner(r io.Reader) *Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return &Scanner{sc: sc}
}

// Next 读取下一个事件。流结束时返回 io.EOF。
// 注释行（以 ':' 开头）与空字段被忽略；空行表示事件结束。
func (s *Scanner) Next() (Event, error) {
	var ev Event
	var dataLines []string
	started := false

	for s.sc.Scan() {
		line := strings.TrimSuffix(s.sc.Text(), "\r")

		if line == "" {
			if !started {
				continue // 事件之间的空行
			}
			ev.Data = strings.Join(dataLines, "\n")
			return ev, nil
		}

		// 注释行（保活 / 调试）不构成事件内容，必须在 started 之前跳过。
		if strings.HasPrefix(line, ":") {
			continue
		}
		started = true

		field, value, ok := strings.Cut(line, ":")
		if !ok {
			field, value = line, ""
		}
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "event":
			ev.Name = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}

	if err := s.sc.Err(); err != nil {
		return Event{}, err
	}
	if !started {
		return Event{}, io.EOF
	}
	// 流末尾没有空行收尾，仍返回最后一个事件
	ev.Data = strings.Join(dataLines, "\n")
	return ev, nil
}

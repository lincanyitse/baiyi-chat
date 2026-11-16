package sse

import (
	"io"
	"strings"
	"testing"
)

func collect(t *testing.T, input string) []Event {
	t.Helper()
	sc := NewScanner(strings.NewReader(input))
	var out []Event
	for {
		ev, err := sc.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, ev)
	}
}

func TestSingleEvent(t *testing.T) {
	evs := collect(t, "event: message\ndata: {\"a\":1}\n\n")
	if len(evs) != 1 {
		t.Fatalf("事件数 = %d", len(evs))
	}
	if evs[0].Name != "message" || evs[0].Data != `{"a":1}` {
		t.Errorf("event = %+v", evs[0])
	}
}

func TestMultipleEvents(t *testing.T) {
	input := "data: one\n\ndata: two\n\ndata: three\n\n"
	evs := collect(t, input)
	if len(evs) != 3 {
		t.Fatalf("事件数 = %d, 期望 3", len(evs))
	}
	for i, want := range []string{"one", "two", "three"} {
		if evs[i].Data != want {
			t.Errorf("事件 %d data = %q, 期望 %q", i, evs[i].Data, want)
		}
	}
}

func TestMultilineData(t *testing.T) {
	// 多个 data 行应以 \n 连接
	evs := collect(t, "data: line1\ndata: line2\n\n")
	if len(evs) != 1 {
		t.Fatalf("事件数 = %d", len(evs))
	}
	if evs[0].Data != "line1\nline2" {
		t.Errorf("多行 data = %q", evs[0].Data)
	}
}

func TestCRLF(t *testing.T) {
	evs := collect(t, "event: ping\r\ndata: x\r\n\r\n")
	if len(evs) != 1 {
		t.Fatalf("事件数 = %d", len(evs))
	}
	if evs[0].Name != "ping" || evs[0].Data != "x" {
		t.Errorf("event = %+v（CRLF 未正确剥离）", evs[0])
	}
}

func TestCommentsIgnored(t *testing.T) {
	evs := collect(t, ": this is a comment\ndata: real\n\n")
	if len(evs) != 1 || evs[0].Data != "real" {
		t.Errorf("注释应被忽略，实际 %+v", evs)
	}
}

func TestEmptyLinesBetweenEvents(t *testing.T) {
	evs := collect(t, "\n\ndata: a\n\n\n\ndata: b\n\n")
	if len(evs) != 2 {
		t.Fatalf("事件数 = %d, 期望 2", len(evs))
	}
}

func TestFinalEventWithoutTrailingBlankLine(t *testing.T) {
	// 流末尾没有空行收尾，最后一个事件仍应返回
	evs := collect(t, "data: last")
	if len(evs) != 1 {
		t.Fatalf("事件数 = %d", len(evs))
	}
	if evs[0].Data != "last" {
		t.Errorf("data = %q", evs[0].Data)
	}
}

func TestNoSpaceAfterColon(t *testing.T) {
	evs := collect(t, "data:tight\n\n")
	if len(evs) != 1 || evs[0].Data != "tight" {
		t.Errorf("event = %+v", evs)
	}
}

func TestFieldWithoutColon(t *testing.T) {
	// 无冒号的行按字段名处理，值为空
	evs := collect(t, "data\n\n")
	if len(evs) != 1 || evs[0].Data != "" {
		t.Errorf("event = %+v", evs)
	}
}

func TestUnknownFieldsIgnored(t *testing.T) {
	evs := collect(t, "id: 123\nretry: 5000\ndata: payload\n\n")
	if len(evs) != 1 || evs[0].Data != "payload" {
		t.Errorf("未知字段应被忽略，实际 %+v", evs)
	}
}

func TestEmptyInput(t *testing.T) {
	evs := collect(t, "")
	if len(evs) != 0 {
		t.Errorf("空输入应返回 0 个事件，实际 %d", len(evs))
	}
}

func TestOnlyComments(t *testing.T) {
	evs := collect(t, ": a\n: b\n")
	if len(evs) != 0 {
		t.Errorf("只有注释时应返回 0 个事件，实际 %d", len(evs))
	}
}

func TestLargePayload(t *testing.T) {
	// 单行 500KB，验证缓冲区上限足够
	big := strings.Repeat("x", 500*1024)
	evs := collect(t, "data: "+big+"\n\n")
	if len(evs) != 1 {
		t.Fatalf("事件数 = %d", len(evs))
	}
	if len(evs[0].Data) != len(big) {
		t.Errorf("数据被截断: %d != %d", len(evs[0].Data), len(big))
	}
}

func TestJSONWithNewlinesInsideString(t *testing.T) {
	// JSON 字符串里的转义换行不应影响分帧
	input := "data: {\"text\":\"a\\nb\"}\n\n"
	evs := collect(t, input)
	if len(evs) != 1 {
		t.Fatalf("事件数 = %d", len(evs))
	}
	if evs[0].Data != `{"text":"a\nb"}` {
		t.Errorf("data = %q", evs[0].Data)
	}
}

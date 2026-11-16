package stream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sseServer 起一个真实 HTTP 服务，把某个日志以 SSE 暴露。
func sseServer(t *testing.T, log *Log) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		Serve(w, r, log, ParseLastEventID(r), t.Logf)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// sseEvent 是从流里解析出的一条事件。
type sseEvent struct {
	ID     uint64
	Kind   string
	Data   string
	IsPing bool
}

// readEvents 读取 SSE 流直到满足 stop 条件或超时。
func readEvents(t *testing.T, ctx context.Context, url string, lastEventID uint64, stop func([]sseEvent) bool) []sseEvent {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastEventID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatUint(lastEventID, 10))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type 错误：%s", ct)
	}

	var (
		events []sseEvent
		cur    sseEvent
	)
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.Kind != "" || cur.Data != "" || cur.ID > 0 || cur.IsPing {
				events = append(events, cur)
				if stop != nil && stop(events) {
					return events
				}
				cur = sseEvent{}
			}
		case strings.HasPrefix(line, ":"):
			cur.IsPing = true
			events = append(events, cur)
			cur = sseEvent{}
			if stop != nil && stop(events) {
				return events
			}
		case strings.HasPrefix(line, "id: "):
			cur.ID, _ = strconv.ParseUint(strings.TrimPrefix(line, "id: "), 10, 64)
		case strings.HasPrefix(line, "event: "):
			cur.Kind = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.Data = strings.TrimPrefix(line, "data: ")
		}
	}
	return events
}

func TestSSEStreamsEventsInOrder(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")
	for i := range 5 {
		l.Append("text_delta", fmt.Appendf(nil, `{"text":"段%d"}`, i))
	}
	l.Close(json.RawMessage(`{"status":"done"}`))

	srv := sseServer(t, l)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	events := readEvents(t, ctx, srv.URL+"/events", 0, func(es []sseEvent) bool {
		return len(es) >= 5
	})

	var ids []uint64
	for _, e := range events {
		if e.Kind == "text_delta" {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) != 5 {
		t.Fatalf("应收到 5 条 text_delta，实际 %d", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1]+1 {
			t.Fatalf("序号应连续递增：%v", ids)
		}
	}
}

// TestSSEReconnectResumesFromLastEventID 是 FR-CHN-002 的核心验收：
// 断开后带 Last-Event-ID 重连，从断点续发，不重不漏。
func TestSSEReconnectResumesFromLastEventID(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")
	for i := range 10 {
		l.Append("text_delta", fmt.Appendf(nil, `{"text":"段%d"}`, i))
	}
	l.Close(json.RawMessage(`{"status":"done"}`))

	srv := sseServer(t, l)

	// 第一次连接：只读前 3 条就断开
	ctx1, cancel1 := context.WithCancel(context.Background())
	first := readEvents(t, ctx1, srv.URL+"/events", 0, func(es []sseEvent) bool {
		n := 0
		for _, e := range es {
			if e.Kind == "text_delta" {
				n++
			}
		}
		return n >= 3
	})
	cancel1()

	var lastID uint64
	var firstCount int
	for _, e := range first {
		if e.Kind == "text_delta" {
			lastID = e.ID
			firstCount++
		}
	}
	if firstCount != 3 || lastID != 3 {
		t.Fatalf("第一次应读到 3 条且 id=3，实际 %d 条 id=%d", firstCount, lastID)
	}

	// 第二次连接：带 Last-Event-ID 续读
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()

	second := readEvents(t, ctx2, srv.URL+"/events", lastID, func(es []sseEvent) bool {
		n := 0
		for _, e := range es {
			if e.Kind == "text_delta" {
				n++
			}
		}
		return n >= 7
	})

	var ids []uint64
	for _, e := range second {
		if e.Kind == "text_delta" {
			ids = append(ids, e.ID)
		}
	}

	// 应该拿到 4..10，共 7 条
	if len(ids) != 7 {
		t.Fatalf("续读应拿到 7 条，实际 %d：%v", len(ids), ids)
	}
	if ids[0] != 4 {
		t.Fatalf("续读首条应为 4，实际 %d", ids[0])
	}
	// 无重复：
	seen := map[uint64]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("出现重复序号 %d", id)
		}
		seen[id] = true
	}
	// 无遗漏：
	for want := uint64(4); want <= 10; want++ {
		if !seen[want] {
			t.Fatalf("漏掉了序号 %d", want)
		}
	}
}

func TestSSEResetOnOverflow(t *testing.T) {
	m := NewManager(ManagerOptions{Log: LogOptions{MaxEntries: 3}})
	l := m.Ensure("t1")
	for i := range 10 {
		l.Append("text_delta", fmt.Appendf(nil, `{"text":"段%d"}`, i))
	}
	l.Close(nil)

	srv := sseServer(t, l)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 游标 1 已落在淘汰区（head=7）
	events := readEvents(t, ctx, srv.URL+"/events", 1, func(es []sseEvent) bool {
		for _, e := range es {
			if e.Kind == "reset" {
				return true
			}
		}
		return false
	})

	var found bool
	for _, e := range events {
		if e.Kind != "reset" {
			continue
		}
		found = true
		var r Reset
		if err := json.Unmarshal([]byte(e.Data), &r); err != nil {
			t.Fatalf("reset 载荷非法：%v", err)
		}
		if r.Reason != "buffer_overflow" {
			t.Fatalf("reset 原因错误：%s", r.Reason)
		}
		if r.HeadSeq != 7 {
			t.Fatalf("headSeq 应为 7，实际 %d", r.HeadSeq)
		}
	}
	if !found {
		t.Fatal("应收到 reset 事件")
	}
}

func TestSSESendsTurnFinished(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")
	l.Append("text_delta", []byte(`{"text":"完成"}`))
	l.Close(json.RawMessage(`{"status":"done","turn_id":"t1"}`))

	srv := sseServer(t, l)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	events := readEvents(t, ctx, srv.URL+"/events", 0, func(es []sseEvent) bool {
		for _, e := range es {
			if e.Kind == "turn_finished" {
				return true
			}
		}
		return false
	})

	var got bool
	for _, e := range events {
		if e.Kind == "turn_finished" {
			got = true
			if !strings.Contains(e.Data, `"status":"done"`) {
				t.Fatalf("最终结果内容错误：%s", e.Data)
			}
		}
	}
	if !got {
		t.Fatal("应收到 turn_finished")
	}
}

func TestSSEHeaders(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")
	l.Close(nil)
	srv := sseServer(t, l)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	checks := map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"X-Accel-Buffering": "no",
	}
	for k, want := range checks {
		if got := resp.Header.Get(k); !strings.Contains(got, want) {
			t.Fatalf("%s 应为 %q，实际 %q", k, want, got)
		}
	}
}

func TestParseLastEventID(t *testing.T) {
	cases := []struct {
		header string
		want   uint64
	}{
		{"", 0},
		{"5", 5},
		{"0", 0},
		{"abc", 0},
		{"-1", 0},
		{"999999", 999999},
	}
	for _, c := range cases {
		r, _ := http.NewRequest(http.MethodGet, "/", nil)
		if c.header != "" {
			r.Header.Set("Last-Event-ID", c.header)
		}
		if got := ParseLastEventID(r); got != c.want {
			t.Fatalf("Last-Event-ID=%q 应解析为 %d，实际 %d", c.header, c.want, got)
		}
	}
}

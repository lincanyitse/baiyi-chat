package stream

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAppendAndReplay(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")

	for i := range 5 {
		l.Append("text_delta", []byte(fmt.Sprintf(`{"n":%d}`, i)))
	}

	sub := l.Subscribe(0)
	entries, done, _ := sub.Next(nil)
	if done {
		t.Fatal("不该结束")
	}
	if len(entries) != 5 {
		t.Fatalf("应读到 5 条，实际 %d", len(entries))
	}
	for i, e := range entries {
		if e.Seq != uint64(i+1) {
			t.Fatalf("第 %d 条 seq 应为 %d，实际 %d", i, i+1, e.Seq)
		}
	}
}

func TestSubscribeFromCursor(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")
	for i := range 10 {
		_ = i
		l.Append("text_delta", []byte(`{}`))
	}

	// 从第 3 条之后开始：应拿到 4..10
	sub := l.Subscribe(3)
	entries, _, _ := sub.Next(nil)
	if len(entries) != 7 {
		t.Fatalf("应读到 7 条，实际 %d", len(entries))
	}
	if entries[0].Seq != 4 {
		t.Fatalf("首条 seq 应为 4，实际 %d", entries[0].Seq)
	}
}

func TestLiveAppendWakesSubscriber(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")

	sub := l.Subscribe(0)

	var wg sync.WaitGroup
	wg.Add(1)
	var got []Entry
	go func() {
		defer wg.Done()
		entries, _, _ := sub.Next(nil)
		got = entries
	}()

	time.Sleep(20 * time.Millisecond)
	l.Append("text_delta", []byte(`{"a":1}`))

	waitGroup(t, &wg, time.Second)
	if len(got) != 1 || got[0].Kind != "text_delta" {
		t.Fatalf("应收到 1 条 text_delta，实际 %+v", got)
	}
}

func TestCloseSignalsDone(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")
	l.Append("text_delta", []byte(`{}`))
	l.Close(json.RawMessage(`{"status":"done"}`))

	sub := l.Subscribe(0)
	entries, done, _ := sub.Next(nil)
	if len(entries) != 1 || done {
		t.Fatalf("应先拿到剩余事件：%d, done=%v", len(entries), done)
	}

	_, done, _ = sub.Next(nil)
	if !done {
		t.Fatal("事件读完后应报告结束")
	}
}

func TestAppendAfterCloseIgnored(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")
	l.Close(nil)

	if seq := l.Append("text_delta", []byte(`{}`)); seq != 0 {
		t.Fatalf("关闭后 Append 应返回 0，实际 %d", seq)
	}
	if s := l.Stats(); s.Entries != 0 {
		t.Fatalf("关闭后不该再记录事件，实际 %d 条", s.Entries)
	}
}

func TestRingBufferEvictsOldest(t *testing.T) {
	m := NewManager(ManagerOptions{Log: LogOptions{MaxEntries: 5}})
	l := m.Ensure("t1")

	for range 10 {
		l.Append("text_delta", []byte(`{}`))
	}

	s := l.Stats()
	if s.Entries != 5 {
		t.Fatalf("应只保留 5 条，实际 %d", s.Entries)
	}
	if s.HeadSeq != 5 {
		t.Fatalf("headSeq 应为 5，实际 %d", s.HeadSeq)
	}

	// 从头订阅会落在淘汰区
	sub := l.Subscribe(0)
	reset := sub.Reset()
	if reset == nil || reset.Reason != "buffer_overflow" {
		t.Fatalf("淘汰后从头订阅应触发 reset，实际 %+v", reset)
	}
	if reset.HeadSeq != 5 || reset.Last != 10 {
		t.Fatalf("reset 范围错误：%+v", reset)
	}
}

func TestSubscribeWithinWindowNoReset(t *testing.T) {
	m := NewManager(ManagerOptions{Log: LogOptions{MaxEntries: 5}})
	l := m.Ensure("t1")
	for range 10 {
		l.Append("text_delta", []byte(`{}`))
	}

	// 游标 6 仍在窗口内（head=5），不该 reset
	sub := l.Subscribe(6)
	if sub.Reset() != nil {
		t.Fatalf("窗口内订阅不该 reset：%+v", sub.Reset())
	}
	entries, _, _ := sub.Next(nil)
	if len(entries) != 4 || entries[0].Seq != 7 {
		t.Fatalf("应读到 7..10，实际 %+v", entries)
	}
}

func TestCursorAheadTriggersReset(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")
	l.Append("text_delta", []byte(`{}`))

	// 游标来自旧进程，比当前 seq 还大
	sub := l.Subscribe(999)
	reset := sub.Reset()
	if reset == nil || reset.Reason != "cursor_ahead" {
		t.Fatalf("游标超前应 reset，实际 %+v", reset)
	}
}

func TestByteLimitEviction(t *testing.T) {
	m := NewManager(ManagerOptions{Log: LogOptions{MaxBytes: 100, MaxEntries: 1000}})
	l := m.Ensure("t1")

	payload := make([]byte, 40)
	for range 10 {
		l.Append("text_delta", payload)
	}

	s := l.Stats()
	if s.Bytes > 100 {
		t.Fatalf("字节数应受限，实际 %d", s.Bytes)
	}
	if s.Entries >= 10 {
		t.Fatalf("应淘汰部分事件，实际保留 %d 条", s.Entries)
	}
}

func TestMultipleSubscribersIndependent(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")
	l.Append("text_delta", []byte(`{}`))

	a := l.Subscribe(0)
	b := l.Subscribe(0)

	ea, _, _ := a.Next(nil)
	eb, _, _ := b.Next(nil)
	if len(ea) != 1 || len(eb) != 1 {
		t.Fatal("两个订阅者应各自拿到全部事件")
	}
}

// TestSlowSubscriberDoesNotBlockPublisher 是这条设计的关键不变量：
// 卡住的浏览器不能拖慢模型输出。
func TestSlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")

	// 订阅者只建立、从不读取
	_ = l.Subscribe(0)

	done := make(chan struct{})
	go func() {
		for range 1000 {
			l.Append("text_delta", []byte(`{}`))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("发布者被慢订阅者阻塞")
	}
}

func TestSubscriptionCancel(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")

	sub := l.Subscribe(0)
	cancel := make(chan struct{})
	close(cancel)

	_, done, _ := sub.Next(cancel)
	if !done {
		t.Fatal("取消后应报告结束")
	}
}

func TestManagerSweep(t *testing.T) {
	m := NewManager(ManagerOptions{TTL: 10 * time.Millisecond})
	l := m.Ensure("t1")
	l.Append("text_delta", []byte(`{}`))
	l.Close(nil)

	open := m.Ensure("t2") // 未结束
	open.Append("text_delta", []byte(`{}`))

	time.Sleep(20 * time.Millisecond)
	if n := m.Sweep(time.Now()); n != 1 {
		t.Fatalf("应清理 1 个已结束日志，实际 %d", n)
	}
	if m.Get("t1") != nil {
		t.Fatal("已结束的日志应被清理")
	}
	if m.Get("t2") == nil {
		t.Fatal("未结束的日志不该被清理")
	}
}

func TestManagerStats(t *testing.T) {
	m := NewManager(ManagerOptions{})
	a := m.Ensure("a")
	b := m.Ensure("b")
	a.Append("text_delta", []byte(`{"x":1}`))
	b.Append("text_delta", []byte(`{"x":2}`))

	s := m.Stats()
	if s.Turns != 2 || s.Entries != 2 || s.Bytes != 14 {
		t.Fatalf("统计错误：%+v", s)
	}
}

func TestEnsureReturnsSameLog(t *testing.T) {
	m := NewManager(ManagerOptions{})
	a := m.Ensure("t1")
	b := m.Ensure("t1")
	if a != b {
		t.Fatal("同 turnID 应返回同一个日志")
	}
}

func TestConcurrentAppendAndRead(t *testing.T) {
	m := NewManager(ManagerOptions{})
	l := m.Ensure("t1")

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for range 200 {
			l.Append("text_delta", []byte(`{}`))
		}
		l.Close(nil)
	}()

	go func() {
		defer wg.Done()
		sub := l.Subscribe(0)
		var seen int
		for {
			entries, done, _ := sub.Next(nil)
			seen += len(entries)
			if done {
				if seen != 200 {
					t.Errorf("应读到 200 条，实际 %d", seen)
				}
				return
			}
		}
	}()

	waitGroup(t, &wg, 3*time.Second)
}

func waitGroup(t *testing.T, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("等待超时")
	}
}

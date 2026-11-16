// Package stream 为一次轮次的事件流提供可重放的内存日志。
//
// 为什么需要它：Service 的 StreamCallback 是一次性的——agent 在单
// goroutine 里同步调用它，事件不重放。浏览器刷新、网络抖动、多个
// 标签页都会导致客户端错过一部分 delta。把事件先写进日志、再由
// SSE handler 按游标读取，才能做到「断开重连从断点续发」。
//
// 依赖约束：只依赖标准库与 llm。**不得依赖 session/rag/memory**——
// 它需要被反复打桩测试，反向依赖会让测试必须构造整套业务对象。
package stream

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// Entry 是日志中的一条事件。
type Entry struct {
	Seq  uint64
	Kind string
	Data []byte
	// Size 是 Data 的字节数，用于全局内存核算。
	Size int
}

// Log 是单轮次的事件日志：单写多读。
//
// 发布者（agent 回调）永不阻塞：Append 只加锁追加再唤醒等待者，
// 绝不等待任何订阅者。卡住的浏览器不能拖慢模型输出。
type Log struct {
	turnID string

	mu      sync.Mutex
	seq     uint64
	entries []Entry
	headSeq uint64 // 已被淘汰的最小 seq；0 表示尚未淘汰
	bytes   int
	closed  bool
	final   json.RawMessage
	notify  chan struct{} // 每次 Append 后 close 并换新

	maxEntries int
	maxBytes   int
	createdAt  time.Time
	closedAt   time.Time
}

// LogOptions 配置单轮日志的容量。
type LogOptions struct {
	MaxEntries int
	MaxBytes   int
}

// 默认容量：一次长回答通常 3–8k 个 delta。
const (
	defaultMaxEntries = 20000
	defaultMaxBytes   = 8 << 20 // 8 MiB
)

func newLog(turnID string, opts LogOptions) *Log {
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = defaultMaxEntries
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultMaxBytes
	}
	return &Log{
		turnID:     turnID,
		notify:     make(chan struct{}),
		maxEntries: opts.MaxEntries,
		maxBytes:   opts.MaxBytes,
		createdAt:  time.Now(),
	}
}

// TurnID 返回所属轮次。
func (l *Log) TurnID() string { return l.turnID }

// Append 追加一条事件并唤醒所有订阅者。
//
// 超过容量时从头部淘汰——被淘汰的事件无法重放，订阅者会收到
// 一个 reset 信号（见 Subscribe 的返回值），由前端重拉消息树。
func (l *Log) Append(kind string, data []byte) uint64 {
	l.mu.Lock()

	if l.closed {
		l.mu.Unlock()
		return 0
	}

	l.seq++
	seq := l.seq
	entry := Entry{Seq: seq, Kind: kind, Data: data, Size: len(data)}
	l.entries = append(l.entries, entry)
	l.bytes += entry.Size

	for (len(l.entries) > l.maxEntries || l.bytes > l.maxBytes) && len(l.entries) > 1 {
		dropped := l.entries[0]
		l.entries = l.entries[1:]
		l.bytes -= dropped.Size
		l.headSeq = dropped.Seq
	}

	ch := l.notify
	l.notify = make(chan struct{})
	l.mu.Unlock()

	close(ch) // 唤醒所有等待者
	return seq
}

// Close 标记轮次结束，附带最终结果。之后的 Append 被忽略。
func (l *Log) Close(final json.RawMessage) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.final = final
	l.closedAt = time.Now()
	ch := l.notify
	l.notify = make(chan struct{})
	l.mu.Unlock()
	close(ch)
}

// Closed 报告是否已结束。
func (l *Log) Closed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// Final 返回结束时的最终结果；未结束时为 nil。
func (l *Log) Final() json.RawMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.final
}

// Stats 是日志占用的快照，供健康检查暴露。
type Stats struct {
	Entries int
	Bytes   int
	HeadSeq uint64
	Seq     uint64
}

// Stats 返回当前占用。
func (l *Log) Stats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return Stats{Entries: len(l.entries), Bytes: l.bytes, HeadSeq: l.headSeq, Seq: l.seq}
}

// ErrReset 表示游标已落后于缓冲窗口，调用方应让客户端重拉消息树。
type Reset struct {
	// HeadSeq 是当前可用的最小序号。
	HeadSeq uint64
	// Last 是日志中的最大序号。客户端据此判断还剩多少没收到。
	Last uint64
	// Reason 说明为什么重置。
	Reason string
}

// Subscription 是一个订阅者的读取游标。
type Subscription struct {
	log    *Log
	cursor uint64
	// reset 非 nil 表示首次读取时游标已失效。
	reset *Reset
}

// Subscribe 从 afterSeq 之后开始订阅。
//
// afterSeq 为 0 表示「从头读」——但「头」可能已经被淘汰。若确实
// 淘汰过，仍然返回 Reset：客户端据此知道开头缺了一段，应该改拉
// 已落库的完整消息，而不是把残缺的流当成完整回答。
func (l *Log) Subscribe(afterSeq uint64) *Subscription {
	l.mu.Lock()
	defer l.mu.Unlock()

	sub := &Subscription{log: l, cursor: afterSeq}

	switch {
	case afterSeq == 0 && l.headSeq > 0:
		// 从头读但开头已被淘汰：告知缺口，从当前可用处开始
		sub.reset = &Reset{HeadSeq: l.headSeq, Last: l.seq, Reason: "buffer_overflow"}
		sub.cursor = l.headSeq
	case afterSeq > 0 && l.headSeq > 0 && afterSeq < l.headSeq:
		sub.reset = &Reset{HeadSeq: l.headSeq, Last: l.seq, Reason: "buffer_overflow"}
		sub.cursor = l.headSeq
	case afterSeq > l.seq:
		// 游标来自别的进程（服务重启过）
		sub.reset = &Reset{HeadSeq: l.headSeq, Last: l.seq, Reason: "cursor_ahead"}
		sub.cursor = l.headSeq
	}
	return sub
}

// Reset 返回订阅建立时需要发送的重置信号；nil 表示无需重置。
func (s *Subscription) Reset() *Reset { return s.reset }

// Next 阻塞直到有新事件、轮次结束、或 ctx 取消。
//
// 返回 (entries, done)：done 为 true 表示日志已关闭且事件已读完。
// 调用方应在 done 后关闭连接，避免浏览器无限重连一个已结束的轮次。
func (s *Subscription) Next(cancel <-chan struct{}) ([]Entry, bool, *Reset) {
	return s.NextTimeout(cancel, 0)
}

// NextTimeout 是 Next 的可超时版本。timeout<=0 表示无限等待。
//
// 超时返回 (nil, false, nil) 而不是错误：调用方据此发送心跳后继续等。
// 这样 SSE 层不必在「等事件」与「发心跳」之间做选择，两者都能及时响应。
func (s *Subscription) NextTimeout(cancel <-chan struct{}, timeout time.Duration) ([]Entry, bool, *Reset) {
	for {
		s.log.mu.Lock()
		entries := s.log.entries
		closed := s.log.closed

		// 二分定位游标。事件按 Seq 严格递增，可以二分；
		// 线性扫描在 2 万条日志上每次唤醒都要走一遍，代价不可接受。
		start := sort.Search(len(entries), func(i int) bool {
			return entries[i].Seq > s.cursor
		})

		var batch []Entry
		if start < len(entries) {
			batch = append(batch, entries[start:]...)
			s.cursor = entries[len(entries)-1].Seq
		}
		notify := s.log.notify
		s.log.mu.Unlock()

		if len(batch) > 0 {
			return batch, false, nil
		}
		if closed {
			return nil, true, nil
		}

		if timeout > 0 {
			t := time.NewTimer(timeout)
			select {
			case <-notify:
				t.Stop()
			case <-cancel:
				t.Stop()
				return nil, true, nil
			case <-t.C:
				return nil, false, nil // 超时：交给调用方发心跳
			}
			continue
		}

		select {
		case <-notify:
		case <-cancel:
			return nil, true, nil
		}
	}
}

// Manager 管理所有轮次的日志。
type Manager struct {
	mu   sync.Mutex
	logs map[string]*Log
	opts LogOptions
	ttl  time.Duration
}

// ManagerOptions 配置日志管理器。
type ManagerOptions struct {
	Log LogOptions
	// TTL 是已结束轮次的日志保留时间。
	TTL time.Duration
}

// NewManager 构造管理器。
func NewManager(opts ManagerOptions) *Manager {
	if opts.TTL <= 0 {
		opts.TTL = 10 * time.Minute
	}
	return &Manager{
		logs: make(map[string]*Log),
		opts: opts.Log,
		ttl:  opts.TTL,
	}
}

// Get 按轮次取日志，不存在返回 nil。
func (m *Manager) Get(turnID string) *Log {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.logs[turnID]
}

// Ensure 取或创建日志。
func (m *Manager) Ensure(turnID string) *Log {
	m.mu.Lock()
	defer m.mu.Unlock()

	if l, ok := m.logs[turnID]; ok {
		return l
	}
	l := newLog(turnID, m.opts)
	m.logs[turnID] = l
	return l
}

// Close 结束某个轮次的日志。
// Sweep 清理过期的已结束日志，返回清理数量。
func (m *Manager) Sweep(now time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	removed := 0
	for id, l := range m.logs {
		l.mu.Lock()
		closed := l.closed
		closedAt := l.closedAt
		l.mu.Unlock()

		if closed && now.Sub(closedAt) > m.ttl {
			delete(m.logs, id)
			removed++
		}
	}
	return removed
}

// Stats 汇总所有日志的占用，供健康检查暴露。
type ManagerStats struct {
	Turns   int
	Entries int
	Bytes   int
}

// Stats 返回汇总占用。
func (m *Manager) Stats() ManagerStats {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := ManagerStats{Turns: len(m.logs)}
	for _, l := range m.logs {
		s := l.Stats()
		out.Entries += s.Entries
		out.Bytes += s.Bytes
	}
	return out
}

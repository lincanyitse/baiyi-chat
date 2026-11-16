package stream

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// pingInterval 是心跳间隔。
//
// 反代与浏览器对「长时间无数据」的连接会主动断开，15 秒是常见阈值
// 的安全余量。心跳写成 SSE 注释行（`:` 开头），不占序号、不进缓冲、
// 不触发浏览器的 onmessage。
const pingInterval = 15 * time.Second

// Serve 把日志以 SSE 形式写给 w，直到轮次结束或客户端断开。
//
// afterSeq 来自 Last-Event-ID，0 表示首次连接。
func Serve(
	w http.ResponseWriter, r *http.Request,
	log *Log, afterSeq uint64, logf func(string, ...any),
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "当前服务器不支持流式响应", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// 告诉 nginx 等反代不要缓冲
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sub := log.Subscribe(afterSeq)
	if reset := sub.Reset(); reset != nil {
		if err := writeEvent(w, 0, "reset", reset); err != nil {
			return
		}
		flusher.Flush()
	}

	// 客户端断开：r.Context() 被取消
	cancel := r.Context().Done()

	for {
		// 用带超时的等待：超时即发心跳，有事件就写出去。
		// 两者共用一个循环，避免「等事件」与「发心跳」互相阻塞。
		entries, done, _ := sub.NextTimeout(cancel, pingInterval)

		for _, e := range entries {
			if err := writeRaw(w, e); err != nil {
				logf("SSE 写出失败，客户端可能已断开", "turn", log.TurnID(), "seq", e.Seq, "err", err)
				return
			}
		}

		if done {
			// 轮次结束：把最终结果作为最后一条事件送出，然后关闭。
			// 客户端收到后应主动 close()，不要重连一个已结束的轮次。
			if final := log.Final(); final != nil {
				_ = writeEvent(w, 0, "turn_finished", json.RawMessage(final))
			}
			flusher.Flush()
			return
		}

		if len(entries) == 0 {
			// 超时无事件 → 心跳
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
		}
		flusher.Flush()
	}
}

// writeRaw 写一条已序列化的日志事件。
func writeRaw(w io.Writer, e Entry) error {
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: ", e.Seq, e.Kind); err != nil {
		return err
	}
	if _, err := w.Write(e.Data); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n\n")
	return err
}

// WriteEvent 写一条不带 id 的 SSE 事件。
//
// 导出给同样要说 SSE 的其他端点（如审批流）用：帧格式只有一份，
// 改多行 data 或加 retry 字段时不会漏掉某一处。
func WriteEvent(w io.Writer, kind string, data any) error {
	return writeEvent(w, 0, kind, data)
}

// writeEvent 写一条非日志事件（reset / turn_finished），不带 id。
func writeEvent(w io.Writer, seq uint64, kind string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if seq > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", seq); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: ", kind); err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n\n")
	return err
}

// ParseLastEventID 解析 Last-Event-ID 头。
//
// 非数字或缺失一律当作 0（从头读）。格式错误不该让请求失败——
// 浏览器重连时带的值我们自己写的，出错说明是异常客户端，
// 从头重放是安全的选择。
func ParseLastEventID(r *http.Request) uint64 {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		return 0
	}
	var n uint64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return 0
	}
	return n
}

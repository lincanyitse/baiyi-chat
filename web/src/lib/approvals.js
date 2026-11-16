// 审批事件流。与服务端 /api/approvals/stream 对应。
//
// 服务端只在「审批集合发生变化」时推一次全量，前端用它决定
// 弹窗的增删。首次连接会立刻收到一次当前挂起列表。

/** 订阅审批变化。返回 close()。 */
export function openApprovalStream(onList) {
  const es = new EventSource('/api/approvals/stream');
  let closed = false;

  es.addEventListener('approvals', (ev) => {
    try {
      const list = JSON.parse(ev.data || '[]');
      onList(list);
    } catch {
      // 坏数据忽略，下一个事件会带来正确状态
    }
  });

  es.onerror = () => {
    // EventSource 会自动重连；只有彻底关闭时才通知
    if (es.readyState === EventSource.CLOSED) {
      closed = true;
      es.close();
    }
  };

  return () => {
    if (closed) return;
    closed = true;
    es.close();
  };
}

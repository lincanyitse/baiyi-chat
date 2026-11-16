// SSE 客户端：订阅一轮的事件流并归并成消息块。
//
// 用原生 EventSource：它自动带 Cookie、自动重连并回填
// Last-Event-ID，不需要手写重连逻辑。

/**
 * 把流式事件归并成消息块。
 *
 * 这是前端唯一有状态的复杂逻辑。归并规则：
 * - block_start 按 index 建槽
 * - text_delta / thinking_delta 追加到对应槽
 * - tool_call_ready 就地填充或新建工具卡片
 * - block_stop 封口
 *
 * 关键：turn_finished 到达后，调用方应当用服务端持久化的消息
 * **整体替换**草稿。这样即使中途丢过事件，最终态也与数据库一致。
 */
export class StreamAssembler {
  constructor() {
    this.blocks = [];
    this.usage = null;
    this.stopReason = '';
    this.lastError = '';
    this.finished = false;
    this.final = null;
    // 已累积的正文长度。App 用它判断「内容变了没有」，
    // 不必每个 delta 都对全部块做一次 reduce。
    this.textLen = 0;
  }

  /** 处理一条 SSE 事件。 */
  handle(kind, data) {
    switch (kind) {
      case 'block_start': {
        this.blocks[data.index] = {
          type: data.type,
          text: '',
          closed: false,
        };
        break;
      }
      case 'text_delta':
      case 'thinking_delta': {
        const b = this.blocks[data.index];
        if (b) {
          b.text += data.text;
          this.textLen += data.text.length;
        }
        break;
      }
      case 'block_stop': {
        const b = this.blocks[data.index];
        if (b) b.closed = true;
        break;
      }
      case 'tool_call_ready': {
        // 部分 Provider 会先发 block_start(tool_use)，此时槽位已存在，
        // 就地填充；没发过的（如 OpenAI 兼容协议）才追加新块。
        const card = {
          type: 'tool_use',
          id: data.id,
          name: data.name,
          input: data.input,
          closed: true,
        };
        const slot = this.blocks[data.index];
        if (slot && slot.type === 'tool_use') {
          this.blocks[data.index] = card;
        } else {
          this.blocks.push(card);
        }
        break;
      }
      case 'usage': {
        this.usage = {
          input: data.input,
          output: data.output,
          cached: data.cached,
        };
        break;
      }
      case 'stop': {
        this.stopReason = data.reason;
        break;
      }
      case 'error': {
        // Provider 级错误可能是可重试的，agent 会继续跑；
        // 只记录，最终是否失败以 turn_finished.status 为准。
        this.lastError = data?.error?.message || '流式错误';
        break;
      }
      case 'turn_finished': {
        this.finished = true;
        this.final = data;
        break;
      }
      default:
        // start / ping / tool_args_delta 不影响渲染
        break;
    }
  }

  /** 返回可渲染的块列表（过滤空块）。 */
  visible() {
    return this.blocks.filter((b) => b && (b.text || b.type === 'tool_use'));
  }
}

/** SSE 事件类型清单。与服务端 encodeEvent 的输出一一对应。 */
const EVENT_KINDS = [
  'start',
  'block_start',
  'text_delta',
  'thinking_delta',
  'tool_call_ready',
  'usage',
  'stop',
  'error',
  'reset',
  'turn_finished',
];

/** 连续重连失败多少次后放弃等待。 */
const MAX_RETRIES = 5;

/**
 * 订阅一轮的事件流。
 *
 * handlers:
 *   onEvent(kind, data)  — 收到任意事件
 *   onFinished(final)    — 收到 turn_finished
 *   onReset(reset)       — 缓冲溢出或游标失效，已缺一段，最终以数据库为准
 *   onError(err)         — 连接级错误且重连放弃
 *   onStreamState(state) — 连接状态变化：{ reconnecting: bool, attempts: number }
 *
 * 返回 close()。
 */
export function openTurnStream(turnId, handlers = {}) {
  const es = new EventSource(`/api/turns/${turnId}/events`);
  let closed = false;
  let retries = 0;

  const report = (reconnecting) => {
    handlers.onStreamState?.({ reconnecting, attempts: retries });
  };

  const close = () => {
    if (closed) return;
    closed = true;
    es.close();
  };

  for (const kind of EVENT_KINDS) {
    es.addEventListener(kind, (ev) => {
      if (retries > 0) {
        // 能收到事件说明已经重连成功，撤掉提示
        retries = 0;
        report(false);
      }

      let data = null;
      if (ev.data) {
        try {
          data = JSON.parse(ev.data);
        } catch {
          return;
        }
      }

      handlers.onEvent?.(kind, data);

      if (kind === 'turn_finished') {
        // 轮次已结束：主动关闭，不要自动重连一个已完成的轮次
        close();
        handlers.onFinished?.(data);
        return;
      }
      if (kind === 'reset') {
        handlers.onReset?.(data);
        return;
      }
    });
  }

  es.onopen = () => {
    // EventSource 自动重连成功后也会触发 onopen
    if (retries > 0) {
      retries = 0;
      report(false);
    }
  };

  es.onerror = () => {
    // EventSource 会自动重连；服务端已结束的轮次会 404，
    // 此时重连永远成功不了，靠计数兜底退出。
    if (es.readyState === EventSource.CLOSED) {
      close();
      report(false);
      handlers.onError?.(new Error('连接已断开'));
      return;
    }
    if (++retries > MAX_RETRIES) {
      close();
      report(false);
      handlers.onError?.(new Error('无法连接事件流，请刷新页面'));
      return;
    }
    // 之前这里全程静默，用户只看到回答停住却不知道发生了什么
    report(true);
  };

  return close;
}

// 应用状态。用 Svelte 5 runes 的 $state 放在 .svelte.js 里，
// 各组件直接 import 使用，不引第三方状态库。

import { api } from './api.js';
import { StreamAssembler, openTurnStream } from './sse.js';

/** 会话列表与当前选中。 */
export const sessionsState = $state({
  list: [],
  // total 是服务端匹配到的总数（可能大于 list.length）
  total: 0,
  // showArchived 为 true 时把已归档的会话也列出来
  showArchived: false,
  currentId: '',
  loading: false,
  error: '',
});

/** 当前会话的消息与流式草稿。 */
export const chatState = $state({
  messages: [],
  draft: null, // { blocks, usage, stopReason }
  // 草稿正文的字符总数。流式渲染只应在它变化时重跑 markdown，
  // 而判断「变了没有」不能靠遍历全部块——那本身也是 O(n)。
  draftLen: 0,
  sending: false,
  turnId: '',
  // 最近一轮的 ID。finish 会清空 turnId，但断线/失败后要靠它续跑
  lastTurnId: '',
  error: '',
  // 当前视图的叶子消息。空表示跟随最新一条。
  leaf: '',
  // 正在重新生成的助手消息 ID；期间隐藏该消息的草稿占位
  regeneratingId: '',
  // 待发送的附件（上传成功后返回的元信息）
  pendingAttachments: [],
  // 断线重连状态。EventSource 会自动重连，但以前全程静默，
  // 用户只看到回答停住却不知道发生了什么。
  streamState: { reconnecting: false, attempts: 0 },
});

/** 当前流订阅的收尾句柄；null 表示没有在跑的轮次。 */
let stopCurrent = null;

/** 加载会话列表。 */
export async function loadSessions() {
  sessionsState.loading = true;
  sessionsState.error = '';
  try {
    const res = await api.listSessions({ includeArchived: sessionsState.showArchived });
    // 接口在带 q/offset 时返回 {items,total}，否则是裸数组
    sessionsState.list = Array.isArray(res) ? res : res.items || [];
    sessionsState.total = Array.isArray(res) ? res.length : res.total || 0;
    if (!sessionsState.currentId && sessionsState.list.length > 0) {
      await selectSession(sessionsState.list[0].id);
    }
  } catch (err) {
    sessionsState.error = err.message;
  } finally {
    sessionsState.loading = false;
  }
}

/** 按关键词搜索会话（服务端过滤）。 */
export async function searchSessions(q) {
  sessionsState.error = '';
  try {
    const res = await api.listSessions({ q, includeArchived: sessionsState.showArchived });
    sessionsState.list = Array.isArray(res) ? res : res.items || [];
    sessionsState.total = Array.isArray(res) ? res.length : res.total || 0;
  } catch (err) {
    sessionsState.error = err.message;
  }
}

/** 新建会话并选中。 */
export async function createSession(title = '') {
  const s = await api.createSession(title);
  sessionsState.list = [s, ...sessionsState.list];
  await selectSession(s.id);
  return s;
}

/** 只刷新会话列表（标题可能被服务端自动改过）。失败不影响对话。 */
async function refreshTitles() {
  try {
    sessionsState.list = await api.listSessions();
  } catch {
    // 忽略：标题晚一点刷新不影响使用
  }
}

/** 拉取某个分支的消息链并写进视图。 */
async function loadBranch(sessionId, leaf) {
  chatState.messages = await api.messages(sessionId, leaf);
}

/** 切换会话：加载历史消息。 */
export async function selectSession(id) {
  // 先改 currentId 再停旧流：旧轮次的收尾逻辑靠它判断「已经切走」，
  // 否则它拉回来的消息会覆盖新会话的视图。
  sessionsState.currentId = id;
  chatState.draft = null;
  chatState.error = '';
  chatState.sending = false;
  chatState.turnId = '';
  chatState.lastTurnId = '';
  chatState.streamState = { reconnecting: false, attempts: 0 };
  chatState.leaf = '';
  chatState.regeneratingId = '';
  stopStream();

  try {
    await loadBranch(id, '');
  } catch (err) {
    chatState.messages = [];
    chatState.error = err.message;
  }
}

/** 切换到同一会话的另一条分支。 */
export async function switchBranch(leaf) {
  const sessionId = sessionsState.currentId;
  if (!sessionId || chatState.sending) return;
  chatState.leaf = leaf;
  chatState.error = '';
  try {
    await loadBranch(sessionId, leaf);
  } catch (err) {
    chatState.error = err.message;
  }
}

/** 重命名。 */
export async function renameSession(id, title) {
  const updated = await api.updateSession(id, { title });
  sessionsState.list = sessionsState.list.map((s) => (s.id === id ? updated : s));
}

/**
 * 归档或取消归档。
 *
 * 归档不是删除：会话仍可从「已归档」里找回，只是从主列表消失。
 * 归档后重新拉列表，因为后端默认不返回已归档的。
 */
export async function archiveSession(id, archived) {
  await api.updateSession(id, { archived });
  await loadSessions();
  if (archived && sessionsState.currentId === id) {
    const next = sessionsState.list.find((s) => s.id !== id);
    if (next) {
      await selectSession(next.id);
    } else {
      sessionsState.currentId = '';
      chatState.messages = [];
    }
  }
}

/** 删除会话。 */
export async function deleteSession(id) {
  await api.deleteSession(id);
  sessionsState.list = sessionsState.list.filter((s) => s.id !== id);
  if (sessionsState.currentId === id) {
    sessionsState.currentId = '';
    chatState.messages = [];
    if (sessionsState.list.length > 0) {
      await selectSession(sessionsState.list[0].id);
    }
  }
}

/**
 * 发一条消息并订阅流式响应。
 *
 * 顺序很重要：先建 turn 拿到 id，再订阅 SSE，最后等 turn_finished。
 * 若先订阅后建 turn，会 404；若只建不订阅，就看不到流式输出。
 *
 * 没有当前会话时自动新建一个：用户直接打字发送是最自然的入口，
 * 静默丢弃（早先的行为）等于让消息凭空消失。
 */
export async function sendMessage(text) {
  const hasAttachments = chatState.pendingAttachments.length > 0;
  if (chatState.sending || (!text.trim() && !hasAttachments)) return;

  if (!sessionsState.currentId) {
    await createSession('');
  }

  const attachments = chatState.pendingAttachments;
  chatState.pendingAttachments = [];

  await startTurn({
    create: (sessionId) =>
      api.startTurn(sessionId, text, attachments.map((a) => a.id)),
    // 本地先显示用户消息，等服务端落库后再以拉取的为准
    optimistic: () => ({
      id: `local-${Date.now()}`,
      role: 'user',
      content: [
        ...attachments.map((a) => ({
          type: a.kind === 'image' ? 'image' : 'file',
          text: a.name,
          id: a.id,
          name: a.name,
        })),
        ...(text.trim() ? [{ type: 'text', text }] : []),
      ],
      created_at: Date.now(),
    }),
  });
}

/** 上传一个附件到待发送列表。 */
export async function addAttachment(file) {
  const info = await api.uploadAttachment(file);
  chatState.pendingAttachments = [...chatState.pendingAttachments, info];
  return info;
}

/** 移除待发送附件。 */
export function removeAttachment(id) {
  chatState.pendingAttachments = chatState.pendingAttachments.filter((a) => a.id !== id);
}

/** 重新生成某条助手消息，产生新分支。 */
export async function regenerate(messageId) {
  if (chatState.sending) return;
  await startTurn({
    create: () => api.regenerate(messageId),
    hideMessageId: messageId,
  });
}

/** 编辑一条用户消息，产生新分支。 */
export async function editMessage(messageId, text) {
  if (chatState.sending || !text.trim()) return;
  await startTurn({
    create: () => api.editMessage(messageId, text),
    hideMessageId: messageId,
  });
}

/**
 * 重试上一次失败的提问。
 *
 * 走 editMessage 而不是重新发送：那会产生一条新分支，失败的那次
 * 回答仍留在链上可回看。比「删掉重来」更符合「不修改历史」的原则。
 *
 * 找不到用户消息时返回 false，让调用方知道无法重试。
 */
export async function retryLast() {
  if (chatState.sending) return false;

  for (let i = chatState.messages.length - 1; i >= 0; i--) {
    const m = chatState.messages[i];
    if (m.role !== 'user') continue;
    const text = (m.content || [])
      .filter((b) => b.type === 'text')
      .map((b) => b.text)
      .join('\n')
      .trim();
    if (!text) continue;
    chatState.error = '';
    await editMessage(m.id, text);
    return true;
  }
  return false;
}

/**
 * 发起一轮并跑完。
 *
 * 三个入口只差「怎么建轮次」：发送要多一条乐观用户消息，分支操作
 * 要把被替换的消息先藏起来。其余（状态复位、错误处理、订阅流式）
 * 完全一致，收在一处。
 */
async function startTurn({ create, optimistic, hideMessageId }) {
  chatState.sending = true;
  chatState.error = '';
  chatState.draft = { blocks: [], usage: null, stopReason: '' };
  if (hideMessageId) chatState.regeneratingId = hideMessageId;

  const sessionId = sessionsState.currentId;
  let turnId = '';
  try {
    turnId = (await create(sessionId)).turn_id;
    chatState.turnId = turnId;
    chatState.lastTurnId = turnId;
  } catch (err) {
    chatState.error = err.message;
    chatState.sending = false;
    chatState.draft = null;
    chatState.regeneratingId = '';
    return;
  }

  if (optimistic) chatState.messages = [...chatState.messages, optimistic()];

  await runStream(sessionId, turnId);
}

/**
 * 订阅一轮事件流直到结束，结束时以服务端消息为准刷新视图。
 *
 * 三种入口（普通发送、重新生成、编辑）共用这一段：它们只有「怎么
 * 创建轮次」不同，流式处理与收尾逻辑完全一致。
 */
async function runStream(sessionId, turnId) {
  chatState.streamState = { reconnecting: false, attempts: 0 };
  await new Promise((resolve) => {
    const asm = new StreamAssembler();
    let settled = false;

    const finish = async () => {
      if (settled) return;
      settled = true;
      stopCurrent = null;
      chatState.sending = false;
      chatState.turnId = '';
      chatState.regeneratingId = '';
      chatState.streamState = { reconnecting: false, attempts: 0 };
      resolve();

      // 期间切走了会话：旧轮次的结果不能写进新会话的视图。
      if (sessionsState.currentId !== sessionId) {
        refreshTitles();
        return;
      }

      // 关键：以服务端持久化的消息为准整体替换，避免丢事件导致
      // UI 残留半截内容。分支操作后 leaf 会变，这里重新走默认叶子。
      // 消息与标题互不依赖，并发拉取省一个 RTT。
      const [msgs] = await Promise.all([
        api.messages(sessionId, '').catch(() => null),
        refreshTitles(),
      ]);
      if (sessionsState.currentId !== sessionId) return;

      if (msgs) {
        chatState.messages = msgs;
        chatState.leaf = '';
      }
      chatState.draft = null;
    };

    // 收尾只有一个句柄：先关流，再走 finish 的统一收尾（它自己会
    // 清掉 stopCurrent，重复调用靠 settled 挡住）。
    const close = openTurnStream(turnId, {
      onEvent(kind, data) {
        asm.handle(kind, data);
        if (sessionsState.currentId !== sessionId) return;
        chatState.draft = {
          blocks: asm.visible(),
          usage: asm.usage,
          stopReason: asm.stopReason,
        };
        chatState.draftLen = asm.textLen;
      },
      onFinished(data) {
        if (data?.status === 'failed' && data?.error) {
          chatState.error = data.error;
        }
        finish();
      },
      onReset() {
        // 缓冲溢出：直接以数据库为准
        finish();
      },
      onStreamState(state) {
        // sse.js 在连接状态变化时才回报；这里再挡一层，
        // 避免每次赋值新对象都触发依赖 streamState 的模板重算
        const cur = chatState.streamState;
        if (cur.reconnecting !== state.reconnecting || cur.attempts !== state.attempts) {
          chatState.streamState = state;
        }
      },
      onError(err) {
        chatState.error = err.message;
        finish();
      },
    });

    stopCurrent = () => {
      close();
      finish();
    };
  });
}

/** 停止当前生成。 */
export async function stopGeneration() {
  const turnId = chatState.turnId;
  if (!turnId) return;
  try {
    await api.stopTurn(turnId);
  } catch (err) {
    chatState.error = err.message;
  }
}

/**
 * 续跑中断的轮次。
 *
 * 「重试」是拿最后一条用户消息另起一轮（会产生新分支）；
 * 「继续生成」是接着原来那轮跑，已完成的工具调用不会重跑。
 * 后端早就有 POST /api/turns/{id}/resume，只是前端一直没接。
 */
export async function resumeTurn() {
  const turnId = chatState.lastTurnId;
  if (!turnId || chatState.sending) return;

  chatState.sending = true;
  chatState.turnId = turnId;
  chatState.error = '';
  try {
    // 查一次状态，顺便确认这个轮次还在；具体能不能续跑由后端判定
    await api.turnStatus(turnId);
    await api.resumeTurn(turnId);
    await runStream(sessionsState.currentId, turnId);
  } catch (err) {
    chatState.error = err.message;
    chatState.sending = false;
    chatState.turnId = '';
  }
}

/** 关闭流订阅（切会话时调用），同时解除发送中的等待。 */
function stopStream() {
  stopCurrent?.();
  stopCurrent = null;
}

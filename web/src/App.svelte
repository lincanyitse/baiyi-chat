<script>
  import SessionList from './components/SessionList.svelte';
  import Message from './components/Message.svelte';
  import Composer from './components/Composer.svelte';
  import Welcome from './components/Welcome.svelte';
  import ApprovalDialog from './components/ApprovalDialog.svelte';
  import AdminPanel from './components/AdminPanel.svelte';
  import LoginGate from './components/LoginGate.svelte';
  import ModelPicker from './components/ModelPicker.svelte';
  import Icon from './components/Icon.svelte';
  import { chatState, sessionsState, loadSessions, retryLast, stopGeneration, resumeTurn } from './lib/store.svelte.js';
  import { api, setUnauthorizedHandler } from './lib/api.js';
  import { tick } from 'svelte';

  let scroller = $state(null);
  let sidebarOpen = $state(false);
  let nearBottom = $state(true);
  let view = $state('chat'); // chat | admin
  // 深链目标：首屏能力卡与顶栏 ? 都通过它落到指定设置页
  let adminTab = $state('capabilities');
  // 每次显式导航递增。AdminPanel 靠它感知「这是一次新的深链」，
  // 即使连点同一个入口也要回到该 tab 首屏
  let navToken = $state(0);
  // 从回答里的引用跳到知识库原文时用
  let citeTarget = $state(null);

  function openAdmin(tab = 'capabilities') {
    adminTab = tab;
    navToken += 1;
    view = 'admin';
  }

  function backToChat() {
    view = 'chat';
    // 引用定位是一次性意图，消费完就清，避免下次进知识库仍高亮旧片段
    citeTarget = null;
  }

  /** 回答里的引用卡要求跳到知识库原文。 */
  function openCitation(detail) {
    citeTarget = detail;
    openAdmin('kb');
  }

  // 消息列表的渲染窗口：默认只画最近 INITIAL_WINDOW 条。
  //
  // 长会话全量渲染会卡（每条要跑 markdown 与代码高亮）。
  // 但真做虚拟滚动要处理动态高度与滚动锚定，代价不成比例——
  // 「先画最近的、往上翻再补」在体验上等价，代码少一个数量级。
  const INITIAL_WINDOW = 40;
  const WINDOW_STEP = 40;
  let windowSize = $state(INITIAL_WINDOW);
  const visibleFrom = $derived(Math.max(0, chatState.messages.length - windowSize));

  // 会话内搜索
  let findOpen = $state(false);
  let findQuery = $state('');
  let findResults = $state(null); // { query, messages }
  let findTimer = null;

  function onFindInput(v) {
    findQuery = v;
    clearTimeout(findTimer);
    const q = v.trim();
    if (!q) {
      findResults = null;
      return;
    }
    findTimer = setTimeout(async () => {
      try {
        findResults = await api.searchMessages(sessionsState.currentId, q);
      } catch {
        findResults = { query: q, messages: [] };
      }
    }, 250);
  }

  function closeFind() {
    findOpen = false;
    findQuery = '';
    findResults = null;
  }

  // 点搜索结果跳到那条消息。命中可能在渲染窗口之外，
  // 先把窗口撑大到能覆盖它。
  function jumpTo(msgId) {
    const idx = chatState.messages.findIndex((m) => m.id === msgId);
    if (idx >= 0 && idx < visibleFrom) {
      windowSize = chatState.messages.length - idx;
    }
    tick().then(() => {
      document.getElementById('msg-' + msgId)?.scrollIntoView({ block: 'center' });
    });
  }

  // snippet 取命中关键词周围的一小段，让用户看懂上下文。
  function snippet(msg, q) {
    const text = (msg.content || [])
      .filter((b) => b.type === 'text')
      .map((b) => b.text)
      .join(' ');
    if (!q) return text.slice(0, 80);
    const i = text.toLowerCase().indexOf(q.toLowerCase());
    if (i < 0) return text.slice(0, 80);
    const start = Math.max(0, i - 24);
    const end = Math.min(text.length, i + q.length + 56);
    return (start > 0 ? '…' : '') + text.slice(start, end) + (end < text.length ? '…' : '');
  }

  async function loadEarlier() {
    const el = scroller;
    // 记下展开前的滚动高度：新内容插在上方，不补偿会让用户
    // 突然跳到中间。
    const beforeHeight = el?.scrollHeight ?? 0;
    const beforeTop = el?.scrollTop ?? 0;

    windowSize += WINDOW_STEP;
    await tick();

    if (el) {
      const delta = el.scrollHeight - beforeHeight;
      el.scrollTop = beforeTop + delta;
      nearBottom = false;
    }
  }

  // 认证门：null 表示还在查状态，避免闪一下登录页
  let auth = $state(null);

  async function refreshAuth() {
    try {
      auth = await api.authStatus();
    } catch {
      // 状态接口本身失败时按「已放行」处理，让主界面自己报错
      auth = { enabled: false, logged_in: true };
    }
  }

  $effect(() => {
    refreshAuth();
  });

  // 全局快捷键。只在聊天视图生效——在设置页按 Esc 不该停掉后台的回答。
  $effect(() => {
    function onKey(e) {
      const typing = /^(INPUT|TEXTAREA)$/.test(e.target.tagName) || e.target.isContentEditable;

      // Ctrl/Cmd+K：聚焦会话搜索
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        const box = document.querySelector('.search input');
        box?.focus();
        box?.select();
        return;
      }

      // Ctrl/Cmd+F：覆盖浏览器的页内查找，改为会话内搜索
      // （长会话有渲染窗口，浏览器查找找不到没渲染的消息）
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'f' && view === 'chat') {
        if (!sessionsState.currentId) return;
        e.preventDefault();
        findOpen = true;
        tick().then(() => document.querySelector('.find-input')?.focus());
        return;
      }

      // Esc：停止当前生成。输入框里有内容时先不抢——
      // 用户可能只是想关掉输入法的候选词。
      if (e.key === 'Escape' && view === 'chat' && !typing) {
        if (findOpen) {
          e.preventDefault();
          closeFind();
          return;
        }
        if (chatState.sending) {
          e.preventDefault();
          stopGeneration();
        }
      }
    }

    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });

  // 会话过期时任何 API 请求都会 401。收到就把认证状态重新拉一遍，
  // 让下面的认证门接管——否则用户只会对着一行「未登录」红字。
  $effect(() => {
    setUnauthorizedHandler(() => {
      auth = { enabled: true, logged_in: false };    });
    return () => setUnauthorizedHandler(null);
  });

  // 分支信息：messageID -> { siblings, index }
  let branches = $state({});

  $effect(() => {
    // 认证状态未确定时不发请求：否则会先拉一次、状态回来后再拉一次，
    // 启动阶段白打两次 /api/sessions。
    if (auth === null) return;
    if (auth.enabled && !auth.logged_in) return;
    loadSessions();
  });

  // 切会话时把渲染窗口收回默认值：上一个会话展开的窗口不该带过来。
  $effect(() => {
    void sessionsState.currentId;
    windowSize = INITIAL_WINDOW;
  });

  // 切会话或切分支后重建切换器数据。
  //
  // 刻意不依赖 messages.length：每轮对话它会变两次（乐观追加、
  // 收尾整体替换），而分支结构只在轮次结束后才可能变——那一次
  // 由 store 里的 leaf 变化自然触发，不需要额外的依赖。
  $effect(() => {
    const sessionId = sessionsState.currentId;
    const leaf = chatState.leaf;
    if (!sessionId) {
      branches = {};
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const tree = await api.tree(sessionId, leaf);
        if (cancelled) return;
        const map = {};
        for (const node of tree.messages || []) {
          if (node.siblings > 1) {
            map[node.id] = { siblings: node.siblings, index: node.index };
          }
        }
        branches = map;
      } catch {
        // 分支信息拿不到不影响主对话
      }
    })();
    return () => (cancelled = true);
  });

  // 只有用户本来就在底部时才自动跟随，否则回看历史会被不断拽回去。
  // 用 store 维护的增量计数，不在每次 delta 里遍历全部块。
  const streamKey = $derived(`${chatState.messages.length}:${chatState.draftLen}`);

  $effect(() => {
    void streamKey;
    if (!scroller || !nearBottom) return;
    tick().then(() => {
      scroller.scrollTop = scroller.scrollHeight;
    });
  });

  function onScroll() {
    if (!scroller) return;
    const gap = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight;
    nearBottom = gap < 80;
  }

  function scrollToBottom() {
    if (!scroller) return;
    scroller.scrollTo({ top: scroller.scrollHeight, behavior: 'smooth' });
  }

  const currentTitle = $derived(
    sessionsState.list.find((s) => s.id === sessionsState.currentId)?.title || '新对话'
  );

  const hasMessages = $derived(
    chatState.messages.length > 0 || chatState.draft !== null
  );

  // 有提问才能重试——重试走「重发上一条用户消息」，没有就无从发起
  const hasRetryable = $derived(
    !chatState.sending && chatState.messages.some((m) => m.role === 'user')
  );

  async function onRetry() {
    const ok = await retryLast();
    if (!ok) chatState.error = '找不到可重试的提问，请手动重新发送。';
  }
</script>

{#if auth?.enabled && !auth.logged_in}
  <LoginGate
    onDone={async () => {
      await refreshAuth();
    }}
  />
{:else if auth === null}
  <div class="booting"></div>
{:else}
<div class="app">
  <div class="sidebar-slot" class:open={sidebarOpen}>
    <SessionList
      onClose={() => (sidebarOpen = false)}
      onOpenAdmin={() => {
        openAdmin('capabilities');
        sidebarOpen = false;
      }}
    />
  </div>
  {#if sidebarOpen}
    <button class="scrim" onclick={() => (sidebarOpen = false)} aria-label="关闭侧栏"
    ></button>
  {/if}

  <main class="main">
    {#if view === 'admin'}
      <header class="topbar">
        <button
          class="menu-btn"
          onclick={() => (sidebarOpen = !sidebarOpen)}
          aria-label="切换侧栏"
        >
          <Icon name="panel" size={18} />
        </button>
        <h1>设置与用量</h1>
        <button class="back" onclick={backToChat}>返回对话</button>
      </header>
      <div class="scroll-area">
        <!-- AdminPanel 常驻，tab 由内部状态持有；深链时经 initialTab
             同步，不重挂载——销毁重建会把加载态与用户正在改的设置全丢掉 -->
        <AdminPanel initialTab={adminTab} navToken={navToken} citeTarget={citeTarget} />
      </div>
    {:else}
      <header class="topbar">
        <button
          class="menu-btn"
          onclick={() => (sidebarOpen = !sidebarOpen)}
          aria-label="切换侧栏"
        >
          <Icon name="panel" size={18} />
        </button>
        <h1>{sessionsState.currentId ? currentTitle : '白泽'}</h1>
        {#if sessionsState.currentId}
          <ModelPicker />
        {/if}
        {#if sessionsState.currentId}
          <button
            class="find-btn"
            onclick={() => (findOpen = !findOpen)}
            title="在对话中搜索（Ctrl/Cmd+F）"
            aria-label="在对话中搜索"
            aria-expanded={findOpen}
          >
            <Icon name="search" size={16} />
          </button>
        {/if}
        <button
          class="find-btn help-btn"
          onclick={() => openAdmin('capabilities')}
          title="能力一览与帮助"
          aria-label="能力一览与帮助"
        >
          <Icon name="help" size={16} />
        </button>
        {#if chatState.draft?.usage}
          <span class="usage" title="本轮 token 用量（输入 / 输出）">
            {chatState.draft.usage.input} / {chatState.draft.usage.output} tokens
          </span>
        {/if}
      </header>

      {#if findOpen}
        <div class="find-bar">
          <Icon name="search" size={14} />
          <input
            class="find-input"
            value={findQuery}
            oninput={(e) => onFindInput(e.currentTarget.value)}
            placeholder="在对话中搜索"
            aria-label="在对话中搜索"
          />
          {#if findResults}
            <span class="find-count">{findResults.messages.length} 条</span>
          {/if}
          <button class="find-close" onclick={closeFind} aria-label="关闭搜索">×</button>
          {#if findResults && findResults.messages.length}
            <div class="find-results">
              {#each findResults.messages.slice(0, 20) as m}
                <button class="find-item" onclick={() => jumpTo(m.id)}>
                  <span class="find-role">{m.role === 'user' ? '你' : '白泽'}</span>
                  <span class="find-text">{snippet(m, findResults.query)}</span>
                </button>
              {/each}
              {#if findResults.messages.length > 20}
                <p class="find-more">还有 {findResults.messages.length - 20} 条，试试更具体的关键词</p>
              {/if}
            </div>
          {:else if findResults && findResults.query}
            <div class="find-results"><p class="find-more">没有匹配</p></div>
          {/if}
        </div>
      {/if}

      <div class="scroll-area" bind:this={scroller} onscroll={onScroll}>
        {#if !sessionsState.currentId && !hasMessages}
          <Welcome
            onOpenSettings={(tab) => openAdmin(tab || 'capabilities')}
            onOpenCapabilities={() => openAdmin('capabilities')}
          />
        {:else}
          <div class="thread">
            {#if visibleFrom > 0}
              <button class="load-earlier" onclick={loadEarlier}>
                加载更早的 {visibleFrom} 条消息
              </button>
            {/if}
            {#each chatState.messages.slice(visibleFrom) as m (m.id)}
              {#if m.id === chatState.regeneratingId}
                <!-- 被重新生成的消息先隐藏，等新回答落库后整体刷新 -->
              {:else}
                <Message
                  message={m}
                  branch={branches[m.id] || null}
                  onOpenCitation={openCitation}
                />
              {/if}
            {/each}

            {#if chatState.draft}
              <Message
                message={{
                  id: 'draft',
                  role: 'assistant',
                  content: chatState.draft.blocks,
                }}
                streaming={chatState.sending}
              />
            {/if}

            {#if chatState.streamState.reconnecting}
              <div class="reconnect" role="status">
                <span class="reconnect-dot"></span>
                连接断开，正在重连…（第 {chatState.streamState.attempts} 次）
                <span class="reconnect-hint">已经生成的内容不会丢失</span>
              </div>
            {/if}

            {#if chatState.error}
              <div class="error">
                <Icon name="alert" size={15} />
                <span class="error-text">{chatState.error}</span>
                {#if hasRetryable}
                  <button class="error-retry" onclick={onRetry} disabled={chatState.sending}>
                    重试
                  </button>
                {/if}
                {#if chatState.lastTurnId && !chatState.sending}
                  <button class="error-retry" onclick={resumeTurn}>继续生成</button>
                {/if}
              </div>
            {/if}

            <div class="tail"></div>
          </div>
        {/if}
      </div>

      {#if !nearBottom}
        <button class="to-bottom" onclick={scrollToBottom} aria-label="回到底部">
          <Icon name="chevronDown" size={16} />
        </button>
      {/if}

      <Composer />
    {/if}
  </main>

  <ApprovalDialog />
</div>
{/if}

<style>
  .booting {
    height: 100%;
    background: var(--bg);
  }

  .app {
    display: flex;
    height: 100%;
    overflow: hidden;
  }

  .sidebar-slot {
    flex: none;
    min-height: 0;
    display: flex;
  }

  .main {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    position: relative;
  }

  .topbar {
    flex: none;
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    padding: var(--sp-3) var(--sp-5);
    border-bottom: 1px solid var(--border);
    background: color-mix(in srgb, var(--bg) 88%, transparent);
    backdrop-filter: blur(8px);
    z-index: 2;
  }
  .topbar h1 {
    flex: 1;
    margin: 0;
    font-size: var(--fs-sm);
    font-weight: 500;
    color: var(--fg-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .usage {
    font-size: var(--fs-xs);
    color: var(--faint);
    font-variant-numeric: tabular-nums;
  }
  .menu-btn {
    display: none;
    background: none;
    border: none;
    padding: 4px;
    border-radius: var(--r-sm);
    color: var(--fg-secondary);
    cursor: pointer;
  }
  .menu-btn:hover {
    background: var(--surface-2);
  }

  .back {
    background: none;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 4px 12px;
    cursor: pointer;
    font-size: var(--fs-xs);
    color: var(--fg-secondary);
  }
  .back:hover {
    background: var(--surface-2);
    color: var(--fg);
  }

  .scroll-area {
    flex: 1;
    overflow-y: auto;
    min-height: 0;
  }

  .thread {
    padding: 0 var(--sp-5);
    max-width: calc(var(--reading-width) + var(--sp-5) * 2);
    margin: 0 auto;
  }

  /* 长会话默认只画最近 N 条，更早的点这里补 */
  .load-earlier {
    display: block;
    width: 100%;
    margin: var(--sp-3) 0;
    padding: 8px;
    background: none;
    border: 1px dashed var(--border-strong);
    border-radius: var(--r-md);
    color: var(--muted);
    font-size: var(--fs-xs);
    cursor: pointer;
  }
  .load-earlier:hover {
    background: var(--surface-hover);
    color: var(--fg-secondary);
  }

  /* 底部留白：让最后一条消息不贴着输入框 */
  .tail {    height: var(--sp-6);
  }

  .reconnect {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--sp-2);
    max-width: var(--reading-width);
    margin: var(--sp-3) auto;
    padding: 8px var(--sp-4);
    background: var(--accent-soft);
    border: 1px solid color-mix(in srgb, var(--accent) 30%, transparent);
    border-radius: var(--r-md);
    color: var(--accent);
    font-size: var(--fs-xs);
  }
  .reconnect-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: currentColor;
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.3;
    }
  }
  .reconnect-hint {
    color: var(--muted);
  }

  .error {
    display: flex;
    align-items: flex-start;
    gap: var(--sp-2);
    max-width: var(--reading-width);
    margin: var(--sp-3) auto;
    padding: var(--sp-3) var(--sp-4);
    background: var(--danger-soft);
    border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent);
    border-radius: var(--r-md);
    color: var(--danger);
    font-size: var(--fs-sm);
  }
  .error-text {
    flex: 1;
    line-height: 1.6;
  }
  .error-retry {
    flex: none;
    padding: 2px 10px;
    background: none;
    border: 1px solid color-mix(in srgb, var(--danger) 40%, transparent);
    border-radius: var(--r-sm);
    color: var(--danger);
    font-size: var(--fs-xs);
    cursor: pointer;
  }
  .error-retry:hover:not(:disabled) {
    background: color-mix(in srgb, var(--danger) 10%, transparent);
  }
  .error-retry:disabled {
    opacity: 0.4;
    cursor: default;
  }

  .to-bottom {
    position: absolute;
    bottom: 6.5rem;
    left: 50%;
    transform: translateX(-50%);
    width: 32px;
    height: 32px;
    border-radius: var(--r-full);
    background: var(--surface);
    border: 1px solid var(--border-strong);
    box-shadow: var(--shadow-md);
    display: grid;
    place-items: center;
    cursor: pointer;
    color: var(--fg-secondary);
    z-index: 3;
  }
  .to-bottom:hover {
    background: var(--surface-hover);
  }

  .scrim {
    display: none;
  }

  /* 窄屏：侧栏变成抽屉 */
  @media (max-width: 860px) {
    .sidebar-slot {
      position: fixed;
      inset: 0 auto 0 0;
      z-index: 20;
      transform: translateX(-100%);
      transition: transform 0.2s ease;
    }
    .sidebar-slot.open {
      transform: translateX(0);
      box-shadow: var(--shadow-lg);
    }
    .scrim {
      display: block;
      position: fixed;
      inset: 0;
      z-index: 10;
      background: rgba(0, 0, 0, 0.35);
      border: none;
      padding: 0;
      cursor: default;
    }
    .menu-btn {
      display: block;
    }
    .thread {
      padding: 0 var(--sp-4);
    }
    .topbar {
      padding: var(--sp-3) var(--sp-4);
    }
  }

  /* 会话内搜索 */
  .find-btn {
    background: none;
    border: none;
    color: var(--muted);
    cursor: pointer;
    padding: 5px;
    border-radius: var(--r-sm);
    display: grid;
    place-items: center;
    margin-left: auto;
  }
  .find-btn:hover {
    background: var(--surface-hover);
    color: var(--fg);
  }

  .find-btn.help-btn {
    margin-left: 0;
  }

  .find-bar {
    position: relative;
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    padding: 6px var(--sp-4);
    border-bottom: 1px solid var(--border);
    background: var(--surface);
    color: var(--muted);
  }
  .find-input {
    flex: 1;
    padding: 5px var(--sp-2);
    border: 1px solid var(--border-strong);
    border-radius: var(--r-sm);
    background: var(--bg);
    color: var(--fg);
    font-size: var(--fs-sm);
    outline: none;
    max-width: 24rem;
  }
  .find-input:focus {
    border-color: var(--accent);
  }
  .find-count {
    font-size: var(--fs-xs);
    color: var(--muted);
  }
  .find-close {
    background: none;
    border: none;
    color: var(--muted);
    font-size: 1.1rem;
    line-height: 1;
    cursor: pointer;
    padding: 2px 6px;
    border-radius: var(--r-sm);
  }
  .find-close:hover {
    background: var(--surface-hover);
    color: var(--fg);
  }

  .find-results {
    position: absolute;
    top: 100%;
    left: var(--sp-4);
    right: var(--sp-4);
    z-index: 30;
    max-height: 18rem;
    overflow-y: auto;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    box-shadow: 0 8px 24px rgb(0 0 0 / 0.18);
    padding: 4px;
  }
  .find-item {
    display: flex;
    align-items: baseline;
    gap: var(--sp-2);
    width: 100%;
    text-align: left;
    padding: 6px 8px;
    border: none;
    border-radius: var(--r-sm);
    background: none;
    color: var(--fg-secondary);
    font-size: var(--fs-xs);
    cursor: pointer;
  }
  .find-item:hover {
    background: var(--surface-hover);
    color: var(--fg);
  }
  .find-role {
    flex: none;
    color: var(--accent);
    font-size: 0.7rem;
  }
  .find-text {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .find-more {
    margin: 4px 8px;
    font-size: var(--fs-xs);
    color: var(--faint);
  }
</style>

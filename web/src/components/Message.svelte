<script>
  import ThinkingBlock from './ThinkingBlock.svelte';
  import Icon from './Icon.svelte';
  import { renderMarkdown } from '../lib/markdown.js';
  import {
    chatState,
    switchBranch,
    regenerate,
    editMessage,
  } from '../lib/store.svelte.js';
  import { api } from '../lib/api.js';

  let { message, streaming = false, branch = null, onOpenCitation = null } = $props();

  const isUser = $derived(message.role === 'user');
  const thinking = $derived((message.content || []).filter((b) => b.type === 'thinking'));
  const texts = $derived((message.content || []).filter((b) => b.type === 'text'));
  const toolUses = $derived((message.content || []).filter((b) => b.type === 'tool_use'));
  const toolResults = $derived((message.content || []).filter((b) => b.type === 'tool_result'));
  const citations = $derived((message.content || []).filter((b) => b.type === 'citation'));
  const images = $derived((message.content || []).filter((b) => b.type === 'image'));
  const files = $derived((message.content || []).filter((b) => b.type === 'file'));

  // 工具调用与结果按 id 配对，前端才有可能显示「调了什么、拿到了什么」
  const toolPairs = $derived.by(() => {
    const results = new Map(toolResults.map((r) => [r.id, r]));
    return toolUses.map((u) => ({ use: u, result: results.get(u.id) || null }));
  });

  let copied = $state(false);
  let editing = $state(false);
  let editText = $state('');
  let editBox = $state(null);
  let activeCite = $state(null);

  /** 事件委托：{@html} 生成的引用标记与复制按钮没有 Svelte 事件绑定。 */
  function onMarkdownClick(e) {
    const cite = e.target.closest?.('[data-cite]');
    if (cite) {
      const index = Number(cite.dataset.cite);
      activeCite = citations.find((c) => Number(c.index) === index) || null;
      return;
    }
    const copy = e.target.closest?.('[data-copy-code]');
    if (copy) {
      const pre = copy.closest('.code-wrap')?.querySelector('pre');
      if (!pre) return;
      navigator.clipboard?.writeText(pre.textContent || '').catch(() => {});
      copy.textContent = '已复制';
      setTimeout(() => (copy.textContent = '复制'), 1600);
    }
  }

  function openCitationDoc() {
    if (!activeCite?.doc_id) return;
    // 只表达「我要看这个引用」的意图，跳到哪个面板由上层决定
    onOpenCitation?.(activeCite);
    activeCite = null;
  }

  const busy = $derived(chatState.sending);

  async function copyAll() {
    const text = texts.map((t) => t.text).join('\n\n');
    try {
      await navigator.clipboard.writeText(text);
      copied = true;
      setTimeout(() => (copied = false), 1600);
    } catch {
      // 剪贴板不可用（非安全上下文）时静默失败，不打断阅读
    }
  }

  function startEdit() {
    editText = texts.map((t) => t.text).join('\n');
    editing = true;
    queueMicrotask(() => {
      editBox?.focus();
      // 光标放到末尾，比全选更不容易误删
      editBox?.setSelectionRange(editText.length, editText.length);
    });
  }

  async function submitEdit() {
    const value = editText.trim();
    if (!value) return;
    editing = false;
    await editMessage(message.id, value);
  }

  function onEditKey(e) {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      submitEdit();
    } else if (e.key === 'Escape') {
      editing = false;
    }
  }

  function toolSummary(input) {
    if (!input) return '';
    if (typeof input === 'string') return input;
    const first = Object.entries(input)[0];
    if (!first) return '';
    const [, v] = first;
    return typeof v === 'string' ? v : JSON.stringify(v);
  }

  // 邻居分支的 leaf。第一次点切换时才查，避免每条消息都发一次请求。
  let leafs = $state(null);

  async function goto(delta) {
    if (!branch) return;
    if (!leafs) {
      try {
        const sibs = await api.siblings(message.id);
        leafs = sibs.map((s) => s.leaf_id);
      } catch {
        return;
      }
    }
    const target = leafs[branch.index - 1 + delta];
    if (target) await switchBranch(target);
  }
</script>

<div
  class="row relative flex gap-[var(--sp-3)] py-[var(--sp-4)] px-0 max-w-[var(--reading-width)] mx-auto w-full"
  class:user={isUser}
  class:assistant={!isUser}
  id={'msg-' + message.id}
>
  {#if isUser}
    {#if editing}
      <div class="w-[min(100%,34rem)] flex flex-col gap-[var(--sp-2)]">
        <textarea
          class="w-full resize-none bg-surface [border:1px_solid_var(--accent)] rounded-md p-[var(--sp-3)] [outline:none] leading-[1.6]"
          bind:this={editBox}
          bind:value={editText}
          onkeydown={onEditKey}
          rows="2"
        ></textarea>
        <div class="flex justify-end gap-[var(--sp-2)]">
          <button class="py-[5px] px-[14px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--border)] bg-surface hover:bg-surface-hover" onclick={() => (editing = false)}>取消</button>
          <button class="py-[5px] px-[14px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--accent)] bg-accent text-accent-fg disabled:opacity-50 disabled:cursor-default" onclick={submitEdit} disabled={!editText.trim()}>发送</button>
        </div>
      </div>
    {:else}
      <div class="bubble max-w-[82%] bg-surface-2 rounded-xl py-[var(--sp-3)] px-[var(--sp-4)] [word-break:break-word] leading-[1.65]">
        {#each images as img}
          <div class="inline-flex items-center gap-[5px] mb-[var(--sp-2)] py-[3px] px-[9px] bg-surface [border:1px_solid_var(--border)] rounded-md u-fs-xs text-fg-secondary">
            {#if img.text}
              <span class="font-mono text-[0.8em]">{img.text}</span>
            {:else}
              <span class="font-mono text-[0.8em]">图片</span>
            {/if}
          </div>
        {/each}
        {#each files as f}
          <div class="inline-flex items-center gap-[5px] mb-[var(--sp-2)] py-[3px] px-[9px] bg-surface [border:1px_solid_var(--border)] rounded-md u-fs-xs text-fg-secondary">
            <Icon name="tool" size={13} />
            <span>{f.name || f.text}</span>
          </div>
        {/each}
        {#each texts as t}
          <div class="whitespace-pre-wrap">{t.text}</div>
        {/each}
      </div>
    {/if}
  {:else}
    <div class="flex-none w-[28px] h-[28px] rounded-full bg-accent-soft text-accent grid place-items-center mt-[2px]" aria-hidden="true">
      <Icon name="sparkle" size={15} />
    </div>

    <div class="content flex-1 min-w-0">
      {#each thinking as t}
        <ThinkingBlock text={t.text} />
      {/each}

      {#each toolPairs as pair}
        <details class="tool-card m-0 mb-[var(--sp-2)] py-[6px] px-[10px] [background:var(--bg-subtle)] [border:1px_solid_var(--border)] rounded-md u-fs-xs text-muted">
          <summary class="flex items-center gap-[6px] cursor-pointer [list-style:none]">
            <Icon name="tool" size={14} />
            <span class="text-fg-secondary font-mono">{pair.use.name}</span>
            {#if pair.result}
              <span class={pair.result.is_error
                ? 'ml-auto px-[6px] py-0 rounded-full bg-danger-soft text-danger text-[0.68rem]'
                : 'ml-auto px-[6px] py-0 rounded-full bg-accent-soft text-accent text-[0.68rem]'}>
                {pair.result.is_error ? '失败' : '已完成'}
              </span>
            {:else}
              <span class="ml-auto px-[6px] py-0 rounded-full bg-surface-2 text-muted text-[0.68rem]">执行中</span>
            {/if}
          </summary>
          {#if toolSummary(pair.use.input)}
            <div class="mt-[6px] pt-[6px] [border-top:1px_solid_var(--border)]">
              <span class="block mb-[3px] text-faint text-[0.68rem]">参数</span>
              <pre class="m-0 py-[4px] px-[6px] bg-surface rounded-sm font-mono text-[0.72rem] whitespace-pre-wrap [word-break:break-all] max-h-[12rem] overflow-y-auto text-fg-secondary">{toolSummary(pair.use.input)}</pre>
            </div>
          {/if}
          {#if pair.result}
            <div class="mt-[6px] pt-[6px] [border-top:1px_solid_var(--border)]">
              <span class="block mb-[3px] text-faint text-[0.68rem]">结果</span>
              <pre class="m-0 py-[4px] px-[6px] bg-surface rounded-sm font-mono text-[0.72rem] whitespace-pre-wrap [word-break:break-all] max-h-[12rem] overflow-y-auto {pair.result.is_error ? 'text-danger' : 'text-fg-secondary'}">{pair.result.text}</pre>
            </div>
          {/if}
        </details>
      {/each}

      {#each texts as t}
        {#if !t.text && streaming}
          <div class="typing"><span></span><span></span><span></span></div>
        {:else if t.text && streaming}
          <!-- 流式期间只显示纯文本：每个 delta 对全量文本重跑 marked 是 O(n²)。 -->
          <div class="whitespace-pre-wrap [word-break:break-word] leading-[1.75]">{t.text}</div>
        {:else if t.text}
          <!-- marked 已关闭原始 HTML 且过滤了链接协议，见 lib/markdown.js -->
          <!-- 容器本身不是交互元素：click 只是事件委托，命中 [data-cite] 的
               是原生 button，键盘行为由浏览器触发；两条 a11y 警告不适用 -->
          <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
          <div class="markdown" onclick={onMarkdownClick}>
            {@html renderMarkdown(t.text, citations)}
          </div>
        {/if}
      {/each}

      {#if activeCite}
        <div class="cite-card my-[var(--sp-2)] py-[10px] px-[12px] bg-brand-soft [border:1px_solid_var(--border)] rounded-md u-fs-xs" role="dialog" aria-label="引用详情">
          <header class="flex items-center justify-between gap-[var(--sp-2)] text-fg">
            <strong>{activeCite.title || '知识库引用'}</strong>
            <button class="[background:none] [border:none] text-muted text-[1rem] cursor-pointer px-[4px] py-0" onclick={() => (activeCite = null)} aria-label="关闭">×</button>
          </header>
          {#if activeCite.heading_path}
            <p class="mt-[4px] mb-0 text-muted">{activeCite.heading_path}</p>
          {/if}
          {#if activeCite.page}
            <p class="mt-[4px] mb-0 text-muted">第 {activeCite.page} 页</p>
          {/if}
          {#if activeCite.snippet}
            <blockquote class="my-[6px] py-[4px] px-[8px] bg-surface rounded-sm text-fg-secondary leading-[1.6]">{activeCite.snippet}</blockquote>
          {/if}
          {#if activeCite.doc_id}
            <button class="[background:none] [border:none] p-0 text-accent text-[length:inherit] cursor-pointer underline" onclick={openCitationDoc}>在知识库中查看原文</button>
          {/if}
        </div>
      {/if}

      {#if streaming && !texts.length && !thinking.length && !toolUses.length}
        <div class="typing"><span></span><span></span><span></span></div>
      {/if}

      {#if !streaming && texts.length}
        <div class="actions flex gap-[var(--sp-1)] mt-[var(--sp-2)]">
          <button class="act inline-flex items-center gap-[5px] [background:none] [border:1px_solid_transparent] rounded-sm py-[3px] px-[8px] cursor-pointer text-muted u-fs-xs enabled:hover:bg-surface-2 enabled:hover:border-border enabled:hover:text-fg-secondary disabled:opacity-40 disabled:cursor-default" onclick={copyAll} title="复制回答">
            <Icon name={copied ? 'check' : 'copy'} size={14} />
            <span>{copied ? '已复制' : '复制'}</span>
          </button>
          <button class="act inline-flex items-center gap-[5px] [background:none] [border:1px_solid_transparent] rounded-sm py-[3px] px-[8px] cursor-pointer text-muted u-fs-xs enabled:hover:bg-surface-2 enabled:hover:border-border enabled:hover:text-fg-secondary disabled:opacity-40 disabled:cursor-default" onclick={() => regenerate(message.id)} disabled={busy} title="重新生成">
            <Icon name="sparkle" size={14} />
            <span>重新生成</span>
          </button>
        </div>
      {/if}

      {#if branch && branch.siblings > 1}
        <div class="inline-flex items-center gap-[2px] mt-[var(--sp-2)] py-px px-[6px] [border:1px_solid_var(--border)] rounded-full text-muted u-fs-xs [font-variant-numeric:tabular-nums]">
          <button
            class="branch-btn grid place-items-center [background:none] [border:none] p-[2px] cursor-pointer text-muted rounded-sm enabled:hover:text-fg enabled:hover:bg-surface-2 disabled:opacity-30 disabled:cursor-default"
            onclick={() => goto(-1)}
            disabled={branch.index <= 1 || busy}
            aria-label="上一条分支"
          >
            <Icon name="chevronRight" size={13} />
          </button>
          <span class="branch-count">{branch.index} / {branch.siblings}</span>
          <button
            class="grid place-items-center [background:none] [border:none] p-[2px] cursor-pointer text-muted rounded-sm enabled:hover:text-fg enabled:hover:bg-surface-2 disabled:opacity-30 disabled:cursor-default"
            onclick={() => goto(1)}
            disabled={branch.index >= branch.siblings || busy}
            aria-label="下一条分支"
          >
            <Icon name="chevronRight" size={13} />
          </button>
        </div>
      {/if}
    </div>
  {/if}

  {#if isUser && !editing && !chatState.sending}
    <div class="user-actions">
      <button class="inline-flex items-center gap-[5px] [background:none] [border:1px_solid_transparent] rounded-sm py-[3px] px-[8px] cursor-pointer text-muted u-fs-xs enabled:hover:bg-surface-2 enabled:hover:border-border enabled:hover:text-fg-secondary" onclick={startEdit} title="编辑并重新提问">
        <Icon name="pencil" size={13} />
        <span>编辑</span>
      </button>
    </div>
  {/if}
</div>


<style>
  .row.user {
    justify-content: flex-end;
    padding-top: var(--sp-5);
  }
  .tool-card summary::-webkit-details-marker {
    display: none;
  }
  .cite-card {
    border-left: 3px solid var(--brand);
  }
  .actions,
  .user-actions {
    opacity: 0.45;
    transition: opacity 0.15s ease;
  }
  @media (hover: none) {
    .actions,
    .user-actions {
      opacity: 1;
    }
  }
  .content:hover .actions,
  .actions:focus-within,
  .row.user:hover .user-actions,
  .user-actions:focus-within {
    opacity: 1;
  }
  .user-actions {
    position: absolute;
    left: calc(100% - 4.5rem);
    bottom: -0.1rem;
    display: flex;
    gap: var(--sp-1);
    margin-top: var(--sp-2);
  }
  .branch-btn:first-child :global(.icon) {
    transform: rotate(180deg);
  }
  .typing {
    display: inline-flex;
    gap: 4px;
    padding: var(--sp-2) 0;
  }
  .typing span {
    width: 6px;
    height: 6px;
    border-radius: var(--r-full);
    background: var(--faint);
    animation: blink 1.3s infinite ease-in-out;
  }
  .typing span:nth-child(2) { animation-delay: 0.18s; }
  .typing span:nth-child(3) { animation-delay: 0.36s; }
  @keyframes blink {
    0%, 70%, 100% { opacity: 0.3; }
    35% { opacity: 1; }
  }
</style>

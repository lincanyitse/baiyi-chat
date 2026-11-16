<script>
  import Icon from './Icon.svelte';
  import {
    sessionsState,
    createSession,
    selectSession,
    deleteSession,
    renameSession,
    archiveSession,
    loadSessions,
    searchSessions,
  } from '../lib/store.svelte.js';

  let { onClose = null, onOpenAdmin = null } = $props();

  let editingId = $state('');
  let editingTitle = $state('');
  let editingInput = $state(null);
  let query = $state('');
  // 展开「更多操作」的会话 id。同一时刻只开一个。
  let menuFor = $state('');

  // 搜索防抖：每敲一个字就发一次请求既浪费也容易乱序
  let searchTimer = null;
  function onSearchInput(v) {
    query = v;
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      searchSessions(query.trim());
    }, 220);
  }

  // 按最后更新时间分组：今天 / 最近 7 天 / 更早
  const DAY = 86400000;
  const groups = $derived.by(() => {
    const now = Date.now();
    const buckets = [
      { label: '今天', items: [] },
      { label: '最近 7 天', items: [] },
      { label: '更早', items: [] },
    ];
    for (const s of sessionsState.list) {
      const age = now - (s.updated_at || 0);
      const idx = age < DAY ? 0 : age < 7 * DAY ? 1 : 2;
      buckets[idx].items.push(s);
    }
    return buckets.filter((b) => b.items.length > 0);
  });

  async function startRename(s) {
    editingId = s.id;
    editingTitle = s.title || '';
    // 下一帧聚焦并全选，省掉再点一次
    queueMicrotask(() => {
      editingInput?.focus();
      editingInput?.select();
    });
  }

  async function commitRename() {
    const id = editingId;
    const title = editingTitle.trim();
    editingId = '';
    if (id && title) {
      await renameSession(id, title);
    }
  }

  function onRenameKey(e) {
    if (e.key === 'Enter' && !e.isComposing) {
      e.preventDefault();
      commitRename();
    } else if (e.key === 'Escape') {
      editingId = '';
    }
  }

  async function onNew() {
    // 传空标题：后端靠「标题为空」在首轮自动命名
    await createSession('');
    onClose?.();
  }

  // 导出走浏览器原生下载：链接直接指向接口，不经过 fetch。
  const exportURL = (id, format) => `/api/sessions/${encodeURIComponent(id)}/export?format=${format}`;
  const menuItem =
    'block w-full text-left py-[6px] px-[10px] [border:none] rounded-sm [background:none] text-fg u-fs-xs cursor-pointer no-underline hover:bg-surface-hover';

  async function onArchive(s, e) {
    e.stopPropagation();
    menuFor = '';
    await archiveSession(s.id, !s.archived);
  }

  async function onDelete(id, e) {
    e.stopPropagation();
    if (!confirm('删除这个会话？')) return;
    await deleteSession(id);
  }

  function onPick(id) {
    selectSession(id);
    onClose?.();
  }
</script>

<aside class="w-[16.5rem] flex-[0_0_16.5rem] flex flex-col [background:var(--bg-subtle)] [border-right:1px_solid_var(--border)] min-h-0">
  <div class="pt-[var(--sp-4)] px-[var(--sp-4)] pb-[var(--sp-2)]">
    <div class="flex items-center gap-[var(--sp-2)]">
      <span class="w-[26px] h-[26px] rounded-md bg-accent text-accent-fg grid place-items-center font-semibold u-fs-sm">白</span>
      <span class="font-semibold tracking-[0.01em]">白泽</span>
    </div>
  </div>

  <button class="mx-[var(--sp-3)] my-[var(--sp-2)] flex items-center gap-[var(--sp-2)] py-[9px] px-[var(--sp-3)] bg-surface [border:1px_solid_var(--border)] rounded-md cursor-pointer u-fs-sm text-fg [transition:background_0.15s_ease,border-color_0.15s_ease] hover:bg-surface-hover hover:border-border-strong" onclick={onNew}>
    <Icon name="plus" size={16} />
    <span>新对话</span>
  </button>

  <div class="mx-[var(--sp-3)] mb-[var(--sp-2)] flex items-center gap-[var(--sp-2)] py-[6px] px-[var(--sp-3)] [background:transparent] [border:1px_solid_transparent] rounded-md text-muted focus-within:bg-surface focus-within:[border-color:var(--border)]">
    <Icon name="search" size={14} />
    <input
      class="flex-1 min-w-0 [background:none] [border:none] [outline:none] u-fs-sm placeholder:text-muted"
      value={query}
      oninput={(e) => onSearchInput(e.currentTarget.value)}
      placeholder="搜索会话"
      aria-label="搜索会话"
    />
  </div>

  <label class="flex items-center gap-[5px] pt-0 px-[var(--sp-2)] pb-[6px] u-fs-xs text-faint cursor-pointer select-none">
    <input
      type="checkbox"
      checked={sessionsState.showArchived}
      onchange={(e) => {
        sessionsState.showArchived = e.currentTarget.checked;
        loadSessions();
      }}
    class="cursor-pointer" />
    <span>显示已归档</span>
  </label>

  <nav class="flex-1 overflow-y-auto pt-0 px-[var(--sp-2)] pb-[var(--sp-4)] min-h-0">
    {#each groups as g (g.label)}
      <div class="mt-[var(--sp-2)]">
        <div class="pt-[var(--sp-2)] px-[var(--sp-2)] pb-[var(--sp-1)] u-fs-xs text-muted font-medium">{g.label}</div>
        {#each g.items as s (s.id)}
          <div
            class="item relative flex items-center gap-[var(--sp-2)] py-[7px] px-[var(--sp-2)] rounded-md cursor-pointer u-fs-sm text-fg-secondary min-h-[32px] hover:bg-surface-hover hover:text-fg"
            class:active={s.id === sessionsState.currentId}
            onclick={() => onPick(s.id)}
            role="button"
            tabindex="0"
            onkeydown={(e) => e.key === 'Enter' && onPick(s.id)}
          >
            {#if editingId === s.id}
              <input
                class="flex-1 bg-surface [border:1px_solid_var(--accent)] rounded-sm py-[2px] px-[6px] u-fs-sm [outline:none]"
                bind:this={editingInput}
                bind:value={editingTitle}
                onblur={commitRename}
                onkeydown={onRenameKey}
              />
            {:else}
              <Icon name="chat" size={15} />
              <span class="title flex-1 overflow-hidden text-ellipsis whitespace-nowrap" class:archived={s.archived}>{s.title || '未命名'}</span>
              <span class="actions flex gap-[2px] opacity-0 [transition:opacity_0.12s_ease]">
                <button
                  class="[background:none] [border:none] text-muted cursor-pointer p-[3px] rounded-sm grid place-items-center hover:bg-surface-2 hover:text-fg"
                  onclick={(e) => {
                    e.stopPropagation();
                    startRename(s);
                  }}
                  title="重命名"
                  aria-label="重命名"
                >
                  <Icon name="pencil" size={13} />
                </button>
                <button
                  class="[background:none] [border:none] text-muted cursor-pointer p-[3px] rounded-sm grid place-items-center hover:bg-surface-2 hover:text-fg"
                  onclick={(e) => {
                    e.stopPropagation();
                    menuFor = menuFor === s.id ? '' : s.id;
                  }}
                  title="更多"
                  aria-label="更多操作"
                  aria-expanded={menuFor === s.id}
                >
                  <Icon name="chevronDown" size={13} />
                </button>
              </span>

              {#if menuFor === s.id}
                <div class="menu absolute right-[var(--sp-2)] top-[calc(100%-4px)] z-20 min-w-[10rem] p-[4px] bg-surface [border:1px_solid_var(--border-strong)] rounded-md [box-shadow:0_6px_20px_rgb(0_0_0/0.16)] flex flex-col" role="menu">
                  <a
                    class={menuItem}
                    role="menuitem"
                    href={exportURL(s.id, 'md')}
                    download
                    onclick={(e) => {
                      e.stopPropagation();
                      menuFor = '';
                    }}
                  >
                    导出为 Markdown
                  </a>
                  <a
                    class={menuItem}
                    role="menuitem"
                    href={exportURL(s.id, 'jsonl')}
                    download
                    onclick={(e) => {
                      e.stopPropagation();
                      menuFor = '';
                    }}
                  >
                    导出为 JSONL
                  </a>
                  <button class={menuItem} role="menuitem" onclick={(e) => onArchive(s, e)}>
                    {s.archived ? '取消归档' : '归档'}
                  </button>
                  <button
                    role="menuitem"
                    class="{menuItem} danger"
                    onclick={(e) => onDelete(s.id, e)}
                  >
                    删除
                  </button>
                </div>
              {/if}
            {/if}
          </div>
        {/each}
      </div>
    {/each}

    {#if sessionsState.list.length === 0 && !sessionsState.loading}
      <p class="text-center text-muted u-fs-sm py-[var(--sp-5)] px-[var(--sp-3)]">{query.trim() ? '没有匹配的会话' : '还没有会话'}</p>
    {/if}
  </nav>

  {#if sessionsState.error}
    <div class="mx-[var(--sp-3)] mt-[var(--sp-2)] mb-[var(--sp-3)] py-[var(--sp-2)] px-[var(--sp-3)] bg-danger-soft text-danger rounded-sm u-fs-xs">{sessionsState.error}</div>
  {/if}

  <button class="flex-none flex items-center gap-[var(--sp-2)] mx-[var(--sp-3)] mb-[var(--sp-3)] py-[8px] px-[var(--sp-3)] [background:none] [border:1px_solid_var(--border)] rounded-md cursor-pointer text-fg-secondary u-fs-sm hover:bg-surface-hover hover:text-fg" onclick={() => onOpenAdmin?.()}>
    <Icon name="tool" size={15} />
    <span>设置与能力一览</span>
  </button>
</aside>


<style>
  /* 选中项要压过 hover 原子类。 */
  .item.active {
    background: var(--surface);
    color: var(--fg);
    font-weight: 500;
    box-shadow: var(--shadow-sm);
  }
  .title.archived {
    opacity: 0.55;
    text-decoration: line-through;
  }
  /* 操作按钮常驻但淡出：hover 才显示的按钮在触屏上等于不存在。 */
  .item:hover .actions,
  .item:focus-within .actions,
  .actions:focus-within {
    opacity: 1;
  }
  @media (hover: none) {
    .actions {
      opacity: 1;
    }
  }
  .danger {
    color: var(--danger);
  }
</style>

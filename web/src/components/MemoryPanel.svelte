<script>
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';
  import { formatDateTime } from '../lib/format.js';

  let facts = $state([]);
  let card = $state('');
  let stats = $state(null);
  let status = $state('');
  let loading = $state(false);
  let error = $state('');
  let expanded = $state(''); // 展开审计链的事实 ID
  let events = $state({});
  let busy = $state('');
  // 正在编辑的事实 ID 与草稿值
  let editing = $state('');
  let editValue = $state('');

  const statuses = [
    { key: '', label: '全部' },
    { key: 'active', label: '生效' },
    { key: 'disputed', label: '待确认' },
    { key: 'archived', label: '已归档' },
    { key: 'deleted', label: '已删除' },
  ];

  $effect(() => {
    void status;
    load();
  });

  async function load() {
    loading = true;
    error = '';
    try {
      facts = await api.memoryFacts(status);
      card = (await api.memoryCard()).card || '';
      stats = await api.memoryStats();
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  async function act(id, action) {
    if (busy) return;
    busy = id;
    error = '';
    try {
      await api.factAction(id, action);
      await load();
      if (expanded === id) await loadEvents(id);
    } catch (err) {
      error = err.message;
    } finally {
      busy = '';
    }
  }

  async function toggleEvents(id) {
    if (expanded === id) {
      expanded = '';
      return;
    }
    expanded = id;
    await loadEvents(id);
  }

  function startEdit(f) {
    editing = f.id;
    editValue = f.value;
  }

  async function commitEdit() {
    const id = editing;
    const v = editValue.trim();
    editing = '';
    if (!id || !v) return;

    busy = id;
    error = '';
    try {
      await api.factAction(id, 'edit', v);
      await load();
    } catch (err) {
      error = err.message;
    } finally {
      busy = '';
    }
  }

  function onEditKey(e) {
    if (e.key === 'Enter' && !e.isComposing) {
      e.preventDefault();
      commitEdit();
    } else if (e.key === 'Escape') {
      editing = '';
    }
  }

  async function loadEvents(id) {
    try {
      events = { ...events, [id]: await api.factEvents(id) };
    } catch {
      events = { ...events, [id]: [] };
    }
  }

  const filterOn =
    '[border:none] bg-surface text-fg [box-shadow:var(--shadow-sm)] py-[5px] px-[12px] rounded-sm cursor-pointer u-fs-sm';
  const filterOff =
    '[border:none] [background:none] text-muted py-[5px] px-[12px] rounded-sm cursor-pointer u-fs-sm';
  const badgeClass = {
    active:
      'u-fs-xs py-0 px-[7px] rounded-full [background:color-mix(in_srgb,var(--success)_15%,transparent)] text-success',
    disputed:
      'u-fs-xs py-0 px-[7px] rounded-full [background:color-mix(in_srgb,var(--warning)_18%,transparent)] text-warning',
    deleted: 'u-fs-xs py-0 px-[7px] rounded-full bg-danger-soft text-danger',
  };
  const badgeDefault = 'u-fs-xs py-0 px-[7px] rounded-full bg-surface-2 text-muted';
  const actBtn =
    '[background:none] [border:1px_solid_transparent] p-[3px] rounded-sm cursor-pointer text-muted grid place-items-center enabled:hover:bg-surface-2 enabled:hover:[border-color:var(--border)] enabled:hover:text-fg disabled:opacity-[0.4] disabled:cursor-default';
  const actDanger =
    '[background:none] [border:1px_solid_transparent] p-[3px] rounded-sm cursor-pointer text-muted grid place-items-center enabled:hover:bg-surface-2 enabled:hover:[border-color:var(--border)] enabled:hover:text-danger disabled:opacity-[0.4] disabled:cursor-default';

  const statusLabel = {
    active: '生效',
    disputed: '待确认',
    superseded: '已被取代',
    archived: '已归档',
    deleted: '已删除',
  };
</script>

<div class="flex flex-col gap-[var(--sp-3)]">
  <div class="flex items-center justify-between gap-[var(--sp-3)] flex-wrap">
    <div class="flex gap-[var(--sp-1)] bg-surface-2 p-[3px] rounded-md">
      {#each statuses as st}
        <button class={status === st.key ? filterOn : filterOff} onclick={() => (status = st.key)}>
          {st.label}
        </button>
      {/each}
    </div>
    {#if stats}
      <span class="text-muted u-fs-xs">
        生效 {stats.active_facts} · 待确认 {stats.disputed} · 情节 {stats.episodes}
      </span>
    {/if}
  </div>

  {#if card}
    <section class="py-[var(--sp-3)] px-[var(--sp-4)] [border:1px_solid_var(--border)] rounded-md [background:var(--bg-subtle)]">
      <h3 class="[margin:0_0_var(--sp-2)] u-fs-sm font-semibold">画像卡 <span class="font-normal u-fs-xs text-muted ml-[var(--sp-2)]">每轮注入稳定前缀（只读预览，不计入访问次数）</span></h3>
      <pre class="m-0 whitespace-pre-wrap font-mono u-fs-xs leading-[1.6] text-fg-secondary">{card}</pre>
    </section>
  {/if}

  {#if error}
    <div class="py-[var(--sp-2)] px-[var(--sp-3)] bg-danger-soft text-danger rounded-sm u-fs-sm">{error}</div>
  {/if}

  <div class="flex flex-col gap-[2px]">
    {#each facts as f (f.id)}
      <div class="py-[var(--sp-2)] px-[var(--sp-3)] [border:1px_solid_var(--border)] rounded-sm bg-surface {f.status === 'deleted' ? 'opacity-[0.6]' : ''}">
        <div class="flex items-center gap-[6px] flex-wrap u-fs-sm">
          <span class="text-muted">{f.subject}</span>
          <span class="text-faint">·</span>
          <span class="font-medium">{f.predicate}</span>
          {#if editing === f.id}
            <input
              class="flex-[1] min-w-[8rem] py-[3px] px-[6px] [border:1px_solid_var(--accent)] rounded-sm [background:var(--bg)] text-fg text-[length:inherit] [outline:none]"
              bind:value={editValue}
              onblur={commitEdit}
              onkeydown={onEditKey}
              aria-label="编辑事实值"
            />
          {:else}
            <span class="flex-[1] min-w-[8rem]">{f.value}</span>
          {/if}
          <span class={badgeClass[f.status] || badgeDefault}>{statusLabel[f.status] || f.status}</span>
          <span class="text-muted u-fs-xs">{f.source_type} · 确认 {f.confirmations} 次 · 访问 {f.access_count}</span>
          <span class="flex-[1]"></span>
          <span class="flex gap-[2px]">
            {#if f.status === 'active' && editing !== f.id}
              <button class={actBtn} onclick={() => startEdit(f)} disabled={busy === f.id} title="编辑">
                <Icon name="pencil" size={13} />
              </button>
            {/if}
            {#if f.status === 'disputed' || f.status === 'archived'}
              <button class={actBtn} onclick={() => act(f.id, 'confirm')} disabled={busy === f.id} title="确认">
                <Icon name="check" size={13} />
              </button>
            {/if}
            {#if f.status === 'active'}
              <button class={actBtn} onclick={() => act(f.id, 'dispute')} disabled={busy === f.id} title="标为待确认">
                <Icon name="alert" size={13} />
              </button>
            {/if}
            {#if f.status === 'deleted'}
              <button class={actBtn} onclick={() => act(f.id, 'restore')} disabled={busy === f.id} title="恢复">
                <Icon name="plus" size={13} />
              </button>
            {:else}
              <button class={actDanger} onclick={() => act(f.id, 'delete')} disabled={busy === f.id} title="删除">
                <Icon name="trash" size={13} />
              </button>
            {/if}
            <button class={actBtn} onclick={() => toggleEvents(f.id)} title="审计链">
              <Icon name={expanded === f.id ? 'chevronDown' : 'chevronRight'} size={13} />
            </button>
          </span>
        </div>

        {#if expanded === f.id}
          <div class="mt-[var(--sp-2)] pt-[var(--sp-2)] [border-top:1px_solid_var(--border)] flex flex-col gap-[3px]">
            {#each events[f.id] || [] as e}
              <div class="flex gap-[var(--sp-2)] u-fs-xs text-muted">
                <span class="[font-variant-numeric:tabular-nums] flex-[0_0_5rem]">{formatDateTime(e.created_at)}</span>
                <span class="font-mono text-fg-secondary">{e.action}</span>
                <span class="flex-[0_0_3rem]">{e.actor}</span>
                {#if e.detail}<span>{e.detail}</span>{/if}
              </div>
            {:else}
              <p class="text-muted u-fs-xs text-center p-[var(--sp-2)]">没有审计记录</p>
            {/each}
          </div>
        {/if}
      </div>
    {:else}
      {#if !loading}
        <p class="text-muted u-fs-sm text-center p-[var(--sp-5)]">还没有记忆。对话中说到偏好或长期信息时会自动记住。</p>
      {/if}
    {/each}
  </div>
</div>

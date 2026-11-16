<script>
  import Icon from './Icon.svelte';
  import { tick } from 'svelte';
  import { api } from '../lib/api.js';
  import { formatDateTime, formatSize } from '../lib/format.js';

  // focusDoc 来自回答里的引用卡：{doc_id, chunk_id, start, end, title}
  let { focusDoc = null } = $props();

  let docs = $state([]);
  let total = $state(0);
  let status = $state('');
  let loading = $state(false);
  let error = $state('');
  let expanded = $state('');
  let chunks = $state({});
  let uploading = $state(false);
  let fileInput = $state(null);

  // 检索测试
  let query = $state('');
  let searching = $state(false);
  let result = $state(null);

  // 向量化进度轮询
  let jobs = $state([]);
  let pollTimer = null;

  const filterBtn = (on) =>
    on
      ? '[border:none] bg-surface text-fg [box-shadow:var(--shadow-sm)] py-[5px] px-[12px] rounded-sm cursor-pointer u-fs-sm'
      : '[border:none] [background:none] text-muted py-[5px] px-[12px] rounded-sm cursor-pointer u-fs-sm';
  const jobTone = {
    done: 'text-success',
    failed: 'text-danger',
    pending: 'text-accent',
    running: 'text-accent',
  };
  const badgeTone = {
    ready: '[background:color-mix(in_srgb,var(--success)_15%,transparent)] text-success',
    failed: 'bg-danger-soft text-danger',
    pending: '[background:color-mix(in_srgb,var(--accent)_15%,transparent)] text-accent',
  };
  const opBtn =
    '[background:none] [border:1px_solid_transparent] p-[3px] rounded-sm cursor-pointer text-muted grid place-items-center hover:bg-surface-2 hover:border-border hover:text-fg';
  const opDanger =
    '[background:none] [border:1px_solid_transparent] p-[3px] rounded-sm cursor-pointer text-muted grid place-items-center hover:bg-surface-2 hover:border-border hover:text-danger';

  const statuses = [
    { key: '', label: '全部' },
    { key: 'ready', label: '就绪' },
    { key: 'pending', label: '入库中' },
    { key: 'failed', label: '失败' },
  ];

  $effect(() => {
    void status;
    load();
  });

  // 从回答的引用跳过来时展开对应文档；滚动交给下面的 effect，
  // 它只在片段数据真正到位后触发，不用定时器猜异步何时完成。
  $effect(() => {
    const target = focusDoc;
    if (!target?.doc_id) return;
    expanded = target.doc_id;
    if (!chunks[target.doc_id]) {
      api
        .documentChunks(target.doc_id)
        .then((list) => {
          chunks = { ...chunks, [target.doc_id]: list };
        })
        .catch(() => {
          chunks = { ...chunks, [target.doc_id]: [] };
        });
    }
  });

  $effect(() => {
    const target = focusDoc;
    if (!target?.chunk_id || !chunks[target.doc_id]) return;
    tick().then(() => {
      const el = document.querySelector(
        `[data-chunk-id="${CSS.escape(target.chunk_id)}"]`
      );
      el?.scrollIntoView({ block: 'center' });
    });
  });

  $effect(() => {
    // 有未完成的任务时才轮询，全部结束后停止
    const active = jobs.some((j) => j.status === 'pending' || j.status === 'running');
    if (active && !pollTimer) {
      pollTimer = setInterval(refreshJobs, 3000);
    } else if (!active && pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
    return () => {
      if (pollTimer) {
        clearInterval(pollTimer);
        pollTimer = null;
      }
    };
  });

  async function load() {
    loading = true;
    error = '';
    try {
      const res = await api.kbDocuments({ status, limit: 50 });
      docs = res.documents || [];
      total = res.total || 0;
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
    refreshJobs();
  }

  async function refreshJobs() {
    try {
      jobs = await api.jobs({ kind: 'rag.embed', limit: 10 });
      // 任务完成时刷新文档状态
      if (jobs.some((j) => j.status === 'done')) await reloadDocs();
    } catch {
      // 任务列表拿不到不影响文档管理
    }
  }

  async function reloadDocs() {
    try {
      const res = await api.kbDocuments({ status, limit: 50 });
      docs = res.documents || [];
      total = res.total || 0;
    } catch {
      // 静默
    }
  }

  async function upload(files) {
    if (!files?.length) return;
    uploading = true;
    error = '';
    try {
      for (const f of files) {
        await api.uploadDocument(f);
      }
      await load();
    } catch (err) {
      error = err.message;
    } finally {
      uploading = false;
      if (fileInput) fileInput.value = '';
    }
  }

  function onFilePick(e) {
    upload(e.currentTarget.files);
  }

  function onDrop(e) {
    e.preventDefault();
    upload(e.dataTransfer.files);
  }

  async function remove(id) {
    if (!confirm('删除这个文档？切片与向量会一并清除。')) return;
    error = '';
    try {
      await api.deleteDocument(id);
      await load();
    } catch (err) {
      error = err.message;
    }
  }

  async function toggleChunks(id) {
    if (expanded === id) {
      expanded = '';
      return;
    }
    expanded = id;
    if (!chunks[id]) {
      try {
        chunks = { ...chunks, [id]: await api.documentChunks(id) };
      } catch {
        chunks = { ...chunks, [id]: [] };
      }
    }
  }

  async function search() {
    const q = query.trim();
    if (!q || searching) return;
    searching = true;
    error = '';
    try {
      result = await api.kbSearch(q, 8);
    } catch (err) {
      error = err.message;
      result = null;
    } finally {
      searching = false;
    }
  }

  const statusLabel = { ready: '就绪', pending: '入库中', failed: '失败', running: '运行中', done: '完成' };
</script>

<div class="flex flex-col gap-[var(--sp-3)]">
  <div class="flex items-center justify-between gap-[var(--sp-3)] flex-wrap">
    <div class="flex gap-[var(--sp-1)] bg-surface-2 p-[3px] rounded-md">
      {#each statuses as st}
        <button class={filterBtn(status === st.key)} onclick={() => (status = st.key)}>
          {st.label}
        </button>
      {/each}
    </div>
    <div>
      <input
        type="file"
        bind:this={fileInput}
        onchange={onFilePick}
        multiple
        accept=".md,.markdown,.txt,.html,.htm"
        hidden
      />
      <button
        class="inline-flex items-center gap-[5px] py-[6px] px-[14px] rounded-md [border:1px_solid_var(--border)] bg-surface cursor-pointer u-fs-sm enabled:hover:bg-surface-hover disabled:opacity-50 disabled:cursor-default"
        onclick={() => fileInput?.click()}
        disabled={uploading}
      >
        <Icon name="plus" size={14} />
        {uploading ? '上传中…' : '上传文档'}
      </button>
    </div>
  </div>

  {#if jobs.length > 0}
    <div class="flex gap-[var(--sp-2)] flex-wrap u-fs-xs">
      {#each jobs.slice(0, 3) as j}
        <span class="py-[2px] px-[8px] rounded-full bg-surface-2 {jobTone[j.status] || 'text-muted'}">
          {j.kind} · {statusLabel[j.status] || j.status}
          {#if j.error}<span class="err">{j.error}</span>{/if}
        </span>
      {/each}
    </div>
  {/if}

  {#if error}
    <div class="py-[var(--sp-2)] px-[var(--sp-3)] bg-danger-soft text-danger rounded-sm u-fs-sm">{error}</div>
  {/if}

  <div
    class="flex flex-col gap-[2px] min-h-[6rem] [border:1px_dashed_transparent] rounded-md p-[var(--sp-1)] hover:border-border"
    role="region"
    aria-label="文档列表"
    ondragover={(e) => e.preventDefault()}
    ondrop={onDrop}
  >
    {#each docs as d (d.id)}
      <div class="py-[var(--sp-2)] px-[var(--sp-3)] [border:1px_solid_var(--border)] rounded-sm bg-surface">
        <div class="flex items-center gap-[var(--sp-2)] flex-wrap u-fs-sm">
          <span class="font-medium">{d.title || d.source}</span>
          <span class="font-mono u-fs-xs text-muted">{d.mime}</span>
          <span class="text-muted u-fs-xs">{d.chunk_count} 片段 · {formatSize(d.size_bytes)} · {formatDateTime(d.created_at)}</span>
          <span class="u-fs-xs py-0 px-[7px] rounded-full {badgeTone[d.status] || 'bg-surface-2 text-muted'}">{statusLabel[d.status] || d.status}</span>
          <span class="flex-1"></span>
          <span class="flex gap-[2px]">
            <button class={opBtn} onclick={() => toggleChunks(d.id)} title="查看片段">
              <Icon name={expanded === d.id ? 'chevronDown' : 'chevronRight'} size={13} />
            </button>
            <button class={opDanger} onclick={() => remove(d.id)} title="删除">
              <Icon name="trash" size={13} />
            </button>
          </span>
        </div>
        {#if d.error}<p class="mt-[var(--sp-1)] mb-0 text-danger u-fs-xs">{d.error}</p>{/if}

        {#if expanded === d.id}
          <div class="mt-[var(--sp-2)] pt-[var(--sp-2)] [border-top:1px_solid_var(--border)] flex flex-col gap-[4px] max-h-[20rem] overflow-y-auto">
            {#each chunks[d.id] || [] as c}
              <div
                class="flex gap-[var(--sp-2)] u-fs-xs text-fg-secondary leading-[1.6] py-[2px] px-[4px] rounded-sm {focusDoc?.doc_id === d.id && focusDoc?.chunk_id === c.id ? 'bg-brand-soft [box-shadow:inset_3px_0_0_var(--brand)]' : ''}"
                data-chunk-id={c.id}
              >
                <span class="flex-[0_0_2rem] text-faint [font-variant-numeric:tabular-nums]">#{c.ord + 1}</span>
                <span class="flex-1 whitespace-pre-wrap">{c.text}</span>
                <span class="flex-[0_0_3.5rem] text-right text-faint">{c.chars} 字</span>
              </div>
            {:else}
              <p class="text-muted u-fs-xs text-center p-[var(--sp-2)]">没有片段</p>
            {/each}
          </div>
        {/if}
      </div>
    {:else}
      {#if !loading}
        <p class="text-muted u-fs-sm text-center p-[var(--sp-5)]">还没有文档。拖拽文件到此处或点「上传文档」。</p>
      {/if}
    {/each}
  </div>

  <section class="py-[var(--sp-3)] px-[var(--sp-4)] [border:1px_solid_var(--border)] rounded-md [background:var(--bg-subtle)]">
    <h3 class="m-0 mb-[var(--sp-2)] u-fs-sm font-semibold">检索测试</h3>
    <div class="flex gap-[var(--sp-2)] mb-[var(--sp-2)]">
      <input
        class="flex-1 py-[7px] px-[var(--sp-3)] [border:1px_solid_var(--border-strong)] rounded-md [background:var(--bg)] [outline:none] u-fs-sm focus:border-accent"
        bind:value={query}
        onkeydown={(e) => e.key === 'Enter' && search()}
        placeholder="输入查询，看能命中什么"
      />
      <button
        class="py-[7px] px-[16px] [border:none] rounded-md bg-accent text-accent-fg cursor-pointer u-fs-sm disabled:opacity-50 disabled:cursor-default"
        onclick={search}
        disabled={searching || !query.trim()}
      >
        {searching ? '检索中…' : '检索'}
      </button>
    </div>

    {#if result}
      {#if result.degraded}
        <p class="m-0 mb-[var(--sp-2)] text-warning u-fs-xs">{result.reason}</p>
      {/if}
      {#each result.hits as h}
        <div class="py-[var(--sp-2)] [border-top:1px_solid_var(--border)]">
          <div class="flex items-baseline gap-[var(--sp-2)] u-fs-xs">
            <span class="font-semibold">{h.title}</span>
            {#if h.heading_path}<span class="text-muted">{h.heading_path}</span>{/if}
            <span class="ml-auto [font-variant-numeric:tabular-nums] text-accent">{(h.score * 100).toFixed(1)}</span>
          </div>
          <p class="mt-[4px] mb-0 u-fs-sm leading-[1.6] text-fg-secondary whitespace-pre-wrap">{h.text}</p>
        </div>
      {:else}
        <p class="text-muted u-fs-xs text-center p-[var(--sp-2)]">没有命中</p>
      {/each}
    {/if}
  </section>
</div>

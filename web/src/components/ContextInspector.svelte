<script>
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';
  import { formatTime } from '../lib/format.js';
  import { sessionsState } from '../lib/store.svelte.js';

  let snapshots = $state([]);
  let selected = $state(null);
  let diff = $state(null);
  let loading = $state(false);
  let error = $state('');

  const sessionId = $derived(sessionsState.currentId);

  $effect(() => {
    if (!sessionId) {
      snapshots = [];
      selected = null;
      diff = null;
      return;
    }
    loadList(sessionId);
  });

  async function loadList(id) {
    loading = true;
    error = '';
    try {
      snapshots = await api.contextSnapshots(id, 30);
      if (snapshots.length > 0) {
        await pick(snapshots[0]);
      } else {
        selected = null;
        diff = null;
      }
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  async function pick(meta) {
    error = '';
    try {
      selected = await api.turnContext(meta.turn_id);
      // diff 失败不影响主视图
      diff = await api.turnContextDiff(meta.turn_id).catch(() => null);
    } catch (err) {
      error = err.message;
    }
  }

  function deltaClass(n) {
    if (n > 0) return 'text-success';
    if (n < 0) return 'text-danger';
    return '';
  }

  // same 没有行色，变化列才用涨跌色。added/removed/changed 的行色盖过涨跌色，和原来 tr.X td 的优先级一致。
  const rowTone = {
    added: 'text-success',
    removed: 'text-danger',
    changed: 'text-fg',
  };

  const thL = 'py-[5px] px-[var(--sp-2)] [border-bottom:1px_solid_var(--border)] text-left u-fs-xs font-medium text-muted';
  const thR = 'py-[5px] px-[var(--sp-2)] [border-bottom:1px_solid_var(--border)] text-right [font-variant-numeric:tabular-nums] u-fs-xs font-medium text-muted';
  const tdL = 'py-[5px] px-[var(--sp-2)] [border-bottom:1px_solid_var(--border)] text-left';
  const tdLStrong = 'py-[5px] px-[var(--sp-2)] [border-bottom:1px_solid_var(--border)] text-left font-semibold';
  const tdR = 'py-[5px] px-[var(--sp-2)] [border-bottom:1px_solid_var(--border)] text-right [font-variant-numeric:tabular-nums]';
  const tdTrim = 'py-[5px] px-[var(--sp-2)] [border-bottom:1px_solid_var(--border)] text-left text-warning u-fs-xs';
  const roundBtn =
    'round flex flex-col items-start gap-[1px] py-[6px] px-[var(--sp-2)] [background:none] [border:1px_solid_transparent] rounded-sm cursor-pointer text-left u-fs-xs text-fg-secondary hover:bg-surface-2';

  function shortModel(ref) {
    if (!ref) return '';
    const i = ref.indexOf('/');
    return i >= 0 ? ref.slice(i + 1) : ref;
  }
</script>

<div class="flex flex-col gap-[var(--sp-3)]">
  {#if !sessionId}
    <p class="text-muted u-fs-sm text-center p-[var(--sp-5)]">先选择一个会话。</p>
  {:else}
    <div class="grid grid-cols-[12rem_1fr] max-[720px]:grid-cols-[1fr] gap-[var(--sp-4)] items-start">
      <aside class="flex flex-col gap-[2px] max-h-[24rem] overflow-y-auto">
        <h3 class="[margin:0_0_var(--sp-2)] u-fs-xs font-medium text-muted">轮次</h3>
        {#each snapshots as m}
          <button
            class={roundBtn}
            class:active={selected?.id === m.id}
            onclick={() => pick(m)}
          >
            <span class="[font-variant-numeric:tabular-nums]">{formatTime(m.created_at)}</span>
            <span class="text-muted">{m.total} tok</span>
            <span class="max-w-full overflow-hidden text-ellipsis whitespace-nowrap text-muted font-mono text-[length:0.7rem]" title={m.model}>{shortModel(m.model)}</span>
          </button>
        {/each}
        {#if snapshots.length === 0 && !loading}
          <p class="text-muted u-fs-xs text-center p-[var(--sp-3)]">
            还没有快照。每发一轮消息，这里就会记录模型当时实际看到的内容。
          </p>
        {/if}
      </aside>

      <section class="min-w-0">
        <details class="mb-[var(--sp-3)] py-[var(--sp-2)] px-[var(--sp-3)] bg-brand-soft [border:1px_solid_var(--border)] rounded-md u-fs-xs text-fg-secondary">
          <summary class="cursor-pointer text-accent font-semibold u-fs-sm mb-[var(--sp-2)]">这些表格在说什么？</summary>
          <div class="pt-[var(--sp-2)] leading-[1.7]">
            <p class="[margin:0_0_var(--sp-2)]">
              每一轮发给模型的内容被分成若干段，下面表格的每一行就是一段，
              名称由服务端给出。模型不是「看到整个会话」，而是看到这些段
              按配额拼出来的内容。
            </p>
            <dl class="grid grid-cols-[5rem_1fr] gap-x-[var(--sp-2)] gap-y-[6px] m-0">
              <dt class="text-fg font-semibold">稳定前缀</dt>
              <dd class="m-0 text-muted">
                前几段每轮都一样，服务端靠它命中模型的前缀缓存。
                这里的内容不能逐轮变化，否则缓存全失效、每次都要重新计费。
              </dd>
              <dt class="text-fg font-semibold">配额</dt>
              <dd class="m-0 text-muted">
                每段允许占用的 token 上限。接近上限时会被裁剪，
                裁剪掉的内容与原因记在「说明」列。
              </dd>
              <dt class="text-fg font-semibold">裁剪原因</dt>
              <dd class="m-0 text-muted">
                比如「丢弃最老的轮次」「摘要压缩」。
                想知道某句话为什么没被模型看到，看这里。
              </dd>
              <dt class="text-fg font-semibold">diff</dt>
              <dd class="m-0 text-muted">和上一轮比，哪一段变了、多用了多少 token。</dd>
            </dl>
          </div>
        </details>
        {#if selected}
          <header class="flex items-baseline justify-between gap-[var(--sp-3)] mb-[var(--sp-3)] flex-wrap">
            <div>
              <span class="font-mono u-fs-sm">{selected.model || '未知模型'}</span>
              <span class="ml-[var(--sp-2)] text-muted u-fs-xs">第 {selected.step + 1} 步 · {formatTime(selected.created_at)}</span>
            </div>
            <span class="[font-variant-numeric:tabular-nums] font-semibold">{selected.total_tokens} tokens</span>
          </header>

          <table class="w-full border-collapse u-fs-sm">
            <thead>
              <tr>
                <th class={thL}>段</th>
                <th class={thR}>token</th>
                <th class={thR}>配额</th>
                <th class={thR}>占比</th>
                <th class={thL}>说明</th>
              </tr>
            </thead>
            <tbody>
              {#each selected.segments as seg}
                <tr>
                  <td class={seg.frozen ? tdLStrong : tdL}>
                    {seg.label || seg.kind}
                    {#if seg.frozen}<span class="ml-[6px] text-[length:0.65rem] py-0 px-[5px] rounded-full bg-accent-soft text-accent font-normal">稳定前缀</span>{/if}
                  </td>
                  <td class={tdR}>{seg.tokens}</td>
                  <td class={tdR}>{seg.quota || '—'}</td>
                  <td class={tdR}>
                    {selected.total_tokens
                      ? ((seg.tokens / selected.total_tokens) * 100).toFixed(1) + '%'
                      : '—'}
                  </td>
                  <td class={tdTrim}>
                    {#each seg.trims || [] as t}
                      <span class="mr-[6px]">{t}</span>
                    {/each}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>

          {#if diff && diff.segments?.length}
            <details class="mt-[var(--sp-4)]" open={diff.has_prev}>
              <summary class="cursor-pointer u-fs-sm text-fg-secondary mb-[var(--sp-2)]">
                与上一轮对比
                {#if !diff.has_prev}<span class="ml-[var(--sp-2)] text-muted u-fs-xs">（首轮，全部新增）</span>{/if}
              </summary>
              <table class="w-full border-collapse u-fs-sm">
                <thead>
                  <tr>
                    <th class={thL}>段</th>
                    <th class={thR}>上轮</th>
                    <th class={thR}>本轮</th>
                    <th class={thR}>变化</th>
                  </tr>
                </thead>
                <tbody>
                  {#each diff.segments as d}
                    <tr>
                      <td class="{tdL} {rowTone[d.status] ?? ''}">{d.kind}</td>
                      <td class="{tdR} {rowTone[d.status] ?? ''}">{d.before}</td>
                      <td class="{tdR} {rowTone[d.status] ?? ''}">{d.after}</td>
                      <td class="{tdR} {rowTone[d.status] || deltaClass(d.delta_tokens)}">
                        {d.delta_tokens > 0 ? '+' : ''}{d.delta_tokens}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </details>
          {/if}
        {:else}
          <p class="text-muted u-fs-sm text-center p-[var(--sp-5)]">选择左侧某一轮查看详情。</p>
        {/if}
      </section>
    </div>
  {/if}

  {#if error}
    <div class="py-[var(--sp-2)] px-[var(--sp-3)] bg-danger-soft text-danger rounded-sm u-fs-sm">{error}</div>
  {/if}
</div>


<style>
  /* 选中项的底色和边框要压过 hover 原子类。 */
  .round.active {
    background: var(--accent-soft);
    border-color: var(--accent);
    color: var(--fg);
  }
</style>

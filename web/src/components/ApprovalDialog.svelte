<script>
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';
  import { openApprovalStream } from '../lib/approvals.js';

  // 挂起中的审批列表。服务端推全量，这里整体替换。
  let pending = $state([]);
  let busy = $state(false);

  $effect(() => {
    const close = openApprovalStream((list) => {
      pending = list || [];
    });
    // 首屏补一次：SSE 建立前可能已有审批
    api.pendingApprovals().then((list) => {
      if (Array.isArray(list)) pending = list;
    }).catch(() => {});
    return close;
  });

  const current = $derived(pending[0] || null);

  // 每个状态写成完整类名，避免 bg-surface 和 bg-accent 同时出现时谁生效取决于生成顺序。
  const riskClass = {
    read: 'u-fs-xs py-[2px] px-[9px] rounded-full bg-surface-2 text-fg-secondary',
    write: 'u-fs-xs py-[2px] px-[9px] rounded-full bg-warning text-[#fff]',
    dangerous: 'u-fs-xs py-[2px] px-[9px] rounded-full bg-danger text-[#fff]',
  };
  const actionBtn =
    'py-[7px] px-[16px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--border)] bg-surface text-fg enabled:hover:bg-surface-hover disabled:opacity-50 disabled:cursor-default';

  function riskLabel(risk) {
    return { read: '只读', write: '写入', dangerous: '危险' }[risk] || risk;
  }

  function prettyArgs(raw) {
    if (!raw) return '';
    try {
      return JSON.stringify(JSON.parse(raw), null, 2);
    } catch {
      return raw;
    }
  }

  async function decide(action) {
    if (!current || busy) return;
    busy = true;
    try {
      await api.decideApproval(current.id, action);
      pending = pending.filter((p) => p.id !== current.id);
    } catch {
      // 失败时保留弹窗，用户可重试；服务端可能已经超时
    } finally {
      busy = false;
    }
  }

  /** 对当前所有挂起项执行同一动作。 */
  async function decideAll(action) {
    if (!pending.length || busy) return;
    busy = true;
    try {
      // 逐条发：后端每条审批有各自的超时与状态，
      // 批量接口没做（单用户场景下挂起项通常只有一两条）
      const ids = pending.map((p) => p.id);
      for (const id of ids) {
        await api.decideApproval(id, action);
      }
      pending = pending.filter((p) => !ids.includes(p.id));
    } catch {
      // 部分成功时保留剩余的，用户可以继续处理
    } finally {
      busy = false;
    }
  }
</script>

{#if current}
  <div
    class="overlay fixed inset-0 z-50 [background:rgba(0,0,0,0.4)] grid place-items-center p-[var(--sp-4)]"
  >
    <div
      class="dialog w-[min(100%,30rem)] bg-surface [border:1px_solid_var(--border)] rounded-lg [box-shadow:var(--shadow-lg)] p-[var(--sp-5)]"
      role="dialog"
      aria-modal="true"
      aria-labelledby="approval-title"
    >
      <header class="flex items-center justify-between mb-[var(--sp-3)]">
        <div class="flex items-center gap-[var(--sp-2)] text-warning">
          <Icon name="alert" size={17} />
          <h2 id="approval-title" class="m-0 text-[length:var(--fs-lg)] font-semibold text-fg">需要确认</h2>
        </div>
        <span class={riskClass[current.risk] || riskClass.read}>{riskLabel(current.risk)}</span>
      </header>

      <p class="m-0 mb-[var(--sp-2)] text-fg-secondary u-fs-sm">
        白泽想调用工具 <code class="font-mono bg-surface-2 py-px px-[6px] rounded-sm text-fg">{current.tool_name}</code>：
      </p>
      {#if current.summary}
        <p class="m-0 mb-[var(--sp-3)] u-fs-sm text-fg">{current.summary}</p>
      {/if}
      {#if current.args && current.args !== 'null'}
        <pre
          class="m-0 mb-[var(--sp-3)] p-[var(--sp-3)] [background:var(--bg-subtle)] [border:1px_solid_var(--border)] rounded-md font-mono u-fs-xs leading-[1.5] max-h-[12rem] overflow-auto whitespace-pre-wrap [word-break:break-all]"
        >{prettyArgs(current.args)}</pre>
      {/if}

      {#if pending.length > 1}
        <p class="m-0 mb-[var(--sp-3)] u-fs-xs text-muted flex items-center gap-[var(--sp-2)] flex-wrap">
          还有 {pending.length - 1} 条待确认
          <button
            class="[background:none] [border:none] p-0 text-accent cursor-pointer u-fs-xs underline disabled:opacity-50 disabled:cursor-default"
            onclick={() => decideAll('once')}
            disabled={busy}
          >
            全部允许一次
          </button>
          <button
            class="[background:none] [border:none] p-0 text-danger cursor-pointer u-fs-xs underline disabled:opacity-50 disabled:cursor-default"
            onclick={() => decideAll('deny')}
            disabled={busy}
          >
            全部拒绝
          </button>
        </p>
      {/if}

      <footer class="flex justify-end gap-[var(--sp-2)] mt-[var(--sp-2)]">
        <button
          class="py-[7px] px-[16px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_color-mix(in_srgb,var(--danger)_35%,transparent)] bg-surface text-danger enabled:hover:bg-surface-hover disabled:opacity-50 disabled:cursor-default"
          onclick={() => decide('deny')}
          disabled={busy}
        >拒绝</button>
        <button
          class="py-[7px] px-[16px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--accent)] bg-accent text-accent-fg enabled:hover:bg-accent-hover disabled:opacity-50 disabled:cursor-default"
          onclick={() => decide('once')}
          disabled={busy}
        >允许一次</button>
        <button class={actionBtn} onclick={() => decide('session')} disabled={busy}>
          本会话允许
        </button>
        {#if current.risk === 'write'}
          <button
            class="py-[7px] px-[16px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--border)] bg-surface text-fg-secondary enabled:hover:bg-surface-hover disabled:opacity-50 disabled:cursor-default"
            onclick={() => decide('forever')}
            disabled={busy}
            title="之后这个工具不再询问，直到接触外部内容"
          >
            永久允许
          </button>
        {/if}
      </footer>
    </div>
  </div>
{/if}

<style>
  /* 动画名会被 Svelte 改写，必须和 keyframes 写在同一个 style 里。 */
  .overlay {
    animation: fade 0.12s ease-out;
  }
  @keyframes fade {
    from {
      opacity: 0;
    }
  }

  .dialog {
    animation: rise 0.15s ease-out;
  }
  @keyframes rise {
    from {
      transform: translateY(8px);
      opacity: 0;
    }
  }
</style>

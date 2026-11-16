<script>
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';

  // 只处理「已设口令，需要登录」。首次设置口令在设置页里做——
  // 未设口令时应用本来就直接可用，不该被登录门拦下。
  let { onDone = null } = $props();

  let password = $state('');
  let error = $state('');
  let busy = $state(false);

  const canSubmit = $derived(password.length > 0 && !busy);

  async function submit(e) {
    e.preventDefault();
    if (!canSubmit) return;

    busy = true;
    error = '';
    try {
      await api.login(password);
      onDone?.();
    } catch (err) {
      // 后端不会透露「口令错」之外的信息，这里原样展示
      error = err.message;
      password = '';
    } finally {
      busy = false;
    }
  }
</script>

<div class="h-full grid place-items-center p-[var(--sp-5)]">
  <form
    class="w-[min(100%,24rem)] flex flex-col gap-[var(--sp-3)] py-[var(--sp-6)] px-[var(--sp-5)] bg-surface [border:1px_solid_var(--border)] rounded-lg [box-shadow:var(--shadow-md)]"
    onsubmit={submit}
  >
    <div
      class="w-[44px] h-[44px] rounded-lg bg-accent text-accent-fg grid place-items-center text-[1.25rem] font-semibold mb-[var(--sp-1)]"
    >
      白
    </div>
    <h1 class="m-0 text-[1.25rem] font-semibold">需要登录</h1>
    <p class="[margin:0_0_var(--sp-2)] text-muted u-fs-sm leading-[1.6]">输入口令以继续。</p>

    <label class="flex flex-col gap-[var(--sp-1)] u-fs-sm text-fg-secondary">
      <span>口令</span>
      <!-- svelte-ignore a11y_autofocus — 登录页只有这一个输入框，聚焦是期望行为 -->
      <input
        class="py-[9px] px-[var(--sp-3)] [border:1px_solid_var(--border-strong)] rounded-md bg-bg u-field-focus"
        type="password"
        bind:value={password}
        autocomplete="current-password"
        autofocus
      />
    </label>

    {#if error}
      <p class="flex items-center gap-[6px] m-0 u-fs-xs text-danger"><Icon name="alert" size={14} /> {error}</p>
    {/if}

    <button class="u-btn-accent mt-[var(--sp-2)] p-[10px]" type="submit" disabled={!canSubmit}>
      {busy ? '处理中…' : '登录'}
    </button>

    <p class="[margin:var(--sp-2)_0_0] text-center text-muted u-fs-xs">
      忘记口令？在终端运行
      <code class="font-mono bg-surface-2 py-px px-[5px] rounded-sm">baiyi reset-password</code>
    </p>
  </form>
</div>

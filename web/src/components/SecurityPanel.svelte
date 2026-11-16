<script>
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';
  import { checkPassword, MIN_PASSWORD_LEN } from '../lib/password.js';

  let { status = { enabled: false, logged_in: true }, onChanged = null } = $props();

  let oldPassword = $state('');
  let newPassword = $state('');
  let confirmPassword = $state('');
  let busy = $state(false);
  let error = $state('');
  let done = $state('');

  const isSetup = $derived(!status.enabled);

  async function submit(e) {
    e.preventDefault();
    error = '';
    done = '';

    const problem = checkPassword(newPassword, confirmPassword);
    if (problem) {
      error = problem;
      return;
    }

    busy = true;
    try {
      if (isSetup) {
        await api.setupPassword(newPassword);
      } else {
        await api.changePassword(oldPassword, newPassword);
      }
      oldPassword = newPassword = confirmPassword = '';
      done = isSetup ? '口令已设置，下次打开需要登录' : '口令已更新，其他会话已失效';
      onChanged?.();
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<div class="flex flex-col gap-[var(--sp-3)]">
  <section class="u-box p-[var(--sp-4)]">
    <h3 class="flex items-center gap-[var(--sp-2)] [margin:0_0_var(--sp-2)] u-fs-sm font-semibold text-fg">
      <Icon name="alert" size={16} />
      {isSetup ? '设置访问口令' : '修改口令'}
    </h3>
    <p class="[margin:0_0_var(--sp-3)] u-fs-sm text-muted leading-[1.65]">
      {#if isSetup}
        白泽默认不验证身份。设置口令后，同一台机器上的其他人需要输入口令才能看到你的对话。
        这是本机隔离，不是远程安全防护。
      {:else}
        修改后所有已登录的会话都会失效，需要用新口令重新登录。
      {/if}
    </p>

    <form class="flex flex-col gap-[var(--sp-3)] max-w-[22rem]" onsubmit={submit}>
      {#if !isSetup}
        <label class="flex flex-col gap-[4px] u-fs-xs text-muted">
          <span>当前口令</span>
          <input
            class="py-[8px] px-[var(--sp-3)] [border:1px_solid_var(--border-strong)] rounded-md bg-bg text-fg u-field-focus"
            type="password"
            bind:value={oldPassword}
            autocomplete="current-password"
          />
        </label>
      {/if}
      <label class="flex flex-col gap-[4px] u-fs-xs text-muted">
        <span>{isSetup ? '口令' : '新口令'}</span>
        <input
          class="py-[8px] px-[var(--sp-3)] [border:1px_solid_var(--border-strong)] rounded-md bg-bg text-fg u-field-focus"
          type="password"
          bind:value={newPassword}
          placeholder={`至少 ${MIN_PASSWORD_LEN} 个字符`}
          autocomplete="new-password"
        />
      </label>
      <label class="flex flex-col gap-[4px] u-fs-xs text-muted">
        <span>确认口令</span>
        <input
          class="py-[8px] px-[var(--sp-3)] [border:1px_solid_var(--border-strong)] rounded-md bg-bg text-fg u-field-focus"
          type="password"
          bind:value={confirmPassword}
          autocomplete="new-password"
        />
      </label>

      {#if error}
        <p class="flex items-center gap-[6px] m-0 u-fs-xs text-danger"><Icon name="alert" size={14} /> {error}</p>
      {/if}
      {#if done}
        <p class="flex items-center gap-[6px] m-0 u-fs-xs text-success"><Icon name="check" size={14} /> {done}</p>
      {/if}

      <button
        class="u-btn-accent self-start py-[7px] px-[18px]"
        type="submit"
        disabled={busy || !newPassword}
      >
        {busy ? '处理中…' : isSetup ? '设置口令' : '更新口令'}
      </button>
    </form>

    {#if !isSetup}
      <p class="[margin:var(--sp-3)_0_0] u-fs-xs text-muted">
        忘记口令？在终端运行
        <code class="font-mono bg-surface-2 py-px px-[5px] rounded-[3px]">baiyi reset-password</code>
      </p>
    {/if}
  </section>

  <section class="u-box p-[var(--sp-4)]">
    <h3 class="flex items-center gap-[var(--sp-2)] [margin:0_0_var(--sp-2)] u-fs-sm font-semibold text-fg">当前状态</h3>
    <dl class="grid grid-cols-[7rem_1fr] gap-x-[var(--sp-3)] gap-y-[var(--sp-2)] m-0 u-fs-sm">
      <dt class="text-muted">访问口令</dt>
      <dd class="m-0">{status.enabled ? '已启用' : '未启用（本机任何人可用）'}</dd>
      <dt class="text-muted">本次会话</dt>
      <dd class="m-0">{status.logged_in ? '已登录' : '未登录'}</dd>
    </dl>
  </section>
</div>

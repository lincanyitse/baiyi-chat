<script>
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';
  import { sessionsState } from '../lib/store.svelte.js';

  let { onChanged = null } = $props();

  let models = $state([]);
  let defaultRoute = $state('');
  let open = $state(false);
  let busy = $state(false);
  let error = $state('');

  const current = $derived(
    sessionsState.list.find((s) => s.id === sessionsState.currentId) || null
  );

  // 会话没指定就是跟随路由
  const currentRef = $derived(current?.model_ref || '');
  const label = $derived(
    currentRef ? shortRef(currentRef) : defaultRoute ? `默认 · ${shortRef(defaultRoute)}` : '默认'
  );

  function shortRef(ref) {
    // "provider/model" → 只显示 model，provider 作 title
    const i = ref.indexOf('/');
    return i >= 0 ? ref.slice(i + 1) : ref;
  }

  $effect(() => {
    if (!sessionsState.currentId) return;
    load();
  });

  async function load() {
    try {
      const res = await api.configuredModels();
      models = res.models || [];
      defaultRoute = (res.routes?.chat || [])[0] || '';
    } catch {
      // 模型列表拿不到不影响对话，静默降级为只显示当前值
    }
  }

  async function pick(ref) {
    if (!current || busy) return;
    open = false;
    if (ref === currentRef) return;

    busy = true;
    error = '';
    try {
      const updated = await api.updateSession(current.id, { model: ref });
      sessionsState.list = sessionsState.list.map((s) => (s.id === updated.id ? updated : s));
      onChanged?.(updated);
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<div class="relative flex items-center gap-[var(--sp-2)]">
  <button
    class="inline-flex items-center gap-[5px] max-w-[14rem] py-[3px] px-[9px] [background:none] [border:1px_solid_var(--border)] rounded-full text-fg-secondary u-fs-xs cursor-pointer enabled:hover:bg-surface-2 enabled:hover:text-fg disabled:opacity-50 disabled:cursor-default"
    onclick={() => (open = !open)}
    disabled={!current || busy}
    title={currentRef ? `会话指定：${currentRef}` : '跟随路由默认'}
  >
    <Icon name="sparkle" size={13} />
    <span class="overflow-hidden text-ellipsis whitespace-nowrap">{label}</span>
    <Icon name={open ? 'chevronDown' : 'chevronRight'} size={12} />
  </button>

  {#if open}
    <!-- 遮罩：点空白处关闭。用 button 而不是 div——
         键盘用户需要能聚焦并触发它，div + onclick 做不到。 -->
    <button
      class="fixed inset-0 z-40 [background:transparent] [border:none] p-0 cursor-default"
      onclick={() => (open = false)}
      aria-label="关闭模型选择"
      tabindex="-1"
    ></button>
    <div
      class="absolute top-[calc(100%+6px)] right-0 z-[41] w-[min(22rem,80vw)] max-h-[20rem] overflow-y-auto p-[var(--sp-1)] bg-surface [border:1px_solid_var(--border)] rounded-md [box-shadow:var(--shadow-md)]"
    >
      <button
        class="item flex items-center gap-[var(--sp-2)] w-full py-[6px] px-[var(--sp-2)] [background:none] [border:none] rounded-sm cursor-pointer text-left u-fs-sm text-fg hover:bg-surface-hover"
        class:active={!currentRef}
        onclick={() => pick('')}
      >
        <span class="flex-1 truncate font-mono text-[0.85em]">跟随路由默认</span>
        {#if defaultRoute}<span class="text-muted u-fs-xs">{defaultRoute}</span>{/if}
      </button>
      {#each models as m}
        <button
          class="item flex items-center gap-[var(--sp-2)] w-full py-[6px] px-[var(--sp-2)] [background:none] [border:none] rounded-sm cursor-pointer text-left u-fs-sm text-fg hover:bg-surface-hover"
          class:active={m.ref === currentRef}
          onclick={() => pick(m.ref)}
        >
          <span class="flex-1 truncate font-mono text-[0.85em]">{m.model}</span>
          <span class="text-muted u-fs-xs">{m.provider}</span>
          <span class="flex gap-[3px]">
            {#if m.vision}<span class="text-[0.65rem] px-[5px] py-0 rounded-full bg-surface-2 text-muted">视觉</span>{/if}
            {#if m.tools}<span class="text-[0.65rem] px-[5px] py-0 rounded-full bg-surface-2 text-muted">工具</span>{/if}
          </span>
        </button>
      {/each}
      {#if models.length === 0}
        <p class="m-0 p-[var(--sp-3)] text-center text-muted u-fs-xs">配置里还没有模型</p>
      {/if}
    </div>
  {/if}

  {#if error}
    <span class="text-danger u-fs-xs" title={error}>切换失败</span>
  {/if}
</div>

<style>
  /* 选中项的底色要压过 hover 原子类。组件样式不进 layer，后写且带 scoped 属性，能赢。 */
  .item.active {
    background: var(--accent-soft);
  }
</style>

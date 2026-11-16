<script>
  import Icon from './Icon.svelte';
  import ProviderEditor from './ProviderEditor.svelte';
  import SecurityPanel from './SecurityPanel.svelte';
  import ContextInspector from './ContextInspector.svelte';
  import MemoryPanel from './MemoryPanel.svelte';
  import KnowledgePanel from './KnowledgePanel.svelte';
  import SkillsPanel from './SkillsPanel.svelte';
  import MCPPanel from './MCPPanel.svelte';
  import DataPanel from './DataPanel.svelte';
  import AboutPanel from './AboutPanel.svelte';
  import CapabilitiesPanel from './CapabilitiesPanel.svelte';
  import { adminApi, shortTokens, formatCost } from '../lib/admin.js';
  import { api } from '../lib/api.js';
  import { sessionsState } from '../lib/store.svelte.js';
  import { navGroups, itemsOfGroup, navItems } from '../lib/nav.js';

  // initialTab 让首屏的能力卡、顶栏的 ? 入口可以直接落到某个设置页；
  // citeTarget 是回答里点引用时带过来的文档定位信息。
  let { initialTab = 'capabilities', navToken = 0, citeTarget = null } = $props();
  // tab 由组件自己持有：用户在面板内自由切换；外部深链（navToken 变化）
  // 时才同步。依赖 navToken 而不是 initialTab，是为了让连点同一个入口
  // 也能回到该 tab 首屏。
  // svelte-ignore state_referenced_locally
  let tab = $state(initialTab);
  $effect(() => {
    void navToken; // 深链信号：每次显式导航都同步一次
    tab = initialTab;
  });
  let range = $state('today');
  let report = $state(null);
  let providers = $state(null);
  let auth = $state({ enabled: false, logged_in: true });
  let loading = $state(false);
  let error = $state('');

  const ranges = [
    { key: 'today', label: '今天' },
    { key: 'week', label: '本周' },
    { key: 'month', label: '本月' },
    { key: 'all', label: '全部' },
    { key: 'session', label: '本会话' },
  ];

  /** 统一的加载样板：置 loading、清错误、finally 复位。 */
  async function loadInto(setter, fetcher) {
    loading = true;
    error = '';
    try {
      setter(await fetcher());
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  /** 条形宽度：有价按费用占比，未定价按调用次数占比。最大者占满。 */
  function barWidth(m, report) {
    if (!report?.by_model?.length) return 0;
    const value = (x) => (x.priced ? x.cost || 0 : x.calls || 0);
    const max = Math.max(...report.by_model.map(value), 0.000001);
    return Math.max((value(m) / max) * 100, 2); // 最矮 2%，否则看不见
  }

  const loadCost = () =>
    loadInto(
      (v) => (report = v),
      async () => {
        if (range !== 'session') return adminApi.cost(range);

        // 「本会话」：没有选中会话时给空报表，而不是拿全局数据冒充
        if (!sessionsState.currentId) {
          return {
            calls: 0,
            input_tokens: 0,
            output_tokens: 0,
            cached_tokens: 0,
            cost: 0,
            by_model: [],
            no_session: true,
          };
        }

        const s = await adminApi.sessionCost(sessionsState.currentId);
        // 会话接口没有按模型拆分，字段名也对齐到报表的形状
        return {
          calls: s.calls,
          input_tokens: s.input,
          output_tokens: s.output,
          cached_tokens: s.cached,
          cost: s.cost,
          by_model: [],
        };
      }
    );
  const loadProviders = () => loadInto((v) => (providers = v), () => adminApi.providers());

  async function loadAuth() {
    try {
      auth = await api.authStatus();
    } catch (err) {
      error = err.message;
    }
  }

  $effect(() => {
    if (tab === 'cost') {
      void range;
      // 「本会话」要看当前选中的是哪个，切换会话时刷新
      if (range === 'session') void sessionsState.currentId;
      loadCost();
    } else if (tab === 'providers') {
      loadProviders();
    } else if (tab === 'security') {
      loadAuth();
    }
    // 'context' 由 ContextInspector 自己按当前会话加载
  });

  const navItem =
    'nav-item flex items-center gap-[var(--sp-2)] w-full text-left py-[6px] px-[10px] [border:none] rounded-md [background:none] text-fg-secondary u-fs-sm cursor-pointer hover:bg-surface-hover';
  const filterOn =
    '[border:none] bg-surface text-fg [box-shadow:var(--shadow-sm)] py-[5px] px-[14px] rounded-sm cursor-pointer u-fs-sm';
  const filterOff =
    '[border:none] [background:none] text-muted py-[5px] px-[14px] rounded-sm cursor-pointer u-fs-sm';
  const chipOn =
    'max-[900px]:flex-none max-[900px]:py-[5px] max-[900px]:px-[12px] max-[900px]:[border:1px_solid_var(--accent)] max-[900px]:rounded-full max-[900px]:bg-accent-soft max-[900px]:text-accent max-[900px]:u-fs-xs max-[900px]:cursor-pointer';
  const chipOff =
    'max-[900px]:flex-none max-[900px]:py-[5px] max-[900px]:px-[12px] max-[900px]:[border:1px_solid_var(--border)] max-[900px]:rounded-full max-[900px]:bg-surface max-[900px]:text-fg-secondary max-[900px]:u-fs-xs max-[900px]:cursor-pointer';
  const thL = 'py-[var(--sp-2)] px-[var(--sp-3)] [border-bottom:1px_solid_var(--border)] text-left u-fs-xs font-medium text-muted';
  const thR = 'py-[var(--sp-2)] px-[var(--sp-3)] [border-bottom:1px_solid_var(--border)] text-right [font-variant-numeric:tabular-nums] u-fs-xs font-medium text-muted';
  const tdL = 'py-[var(--sp-2)] px-[var(--sp-3)] [border-bottom:1px_solid_var(--border)] text-left';
  const tdR = 'py-[var(--sp-2)] px-[var(--sp-3)] [border-bottom:1px_solid_var(--border)] text-right [font-variant-numeric:tabular-nums]';
</script>

<div class="flex items-stretch gap-[var(--sp-5)] p-[var(--sp-5)] max-w-[72rem] [margin:0_auto] w-full min-h-full max-[900px]:flex-col max-[900px]:gap-[var(--sp-3)] max-[900px]:p-[var(--sp-4)]">
  <div class="flex-[0_0_12rem] flex flex-col gap-[var(--sp-4)] pr-[var(--sp-4)] [border-right:1px_solid_var(--border)] max-[900px]:hidden">
    <div class="flex flex-col gap-[2px] mb-[var(--sp-2)]">
      <span class="font-brand text-[length:1.1rem] tracking-[0.3em] text-brand">白泽</span>
      <span class="u-fs-xs text-faint">设置与用量</span>
    </div>
    {#each navGroups as group}
      <div class="flex flex-col gap-[2px]">
        <div class="mb-[var(--sp-1)] text-[length:0.68rem] tracking-[0.08em] text-faint">{group.label}</div>
        {#each itemsOfGroup(group.key) as it}
          <button
            class={navItem}
            class:active={tab === it.key}
            onclick={() => (tab = it.key)}
            title={it.desc}
          >
            <Icon name={it.icon} size={15} />
            <span>{it.label}</span>
          </button>
        {/each}
      </div>
    {/each}
  </div>

  <div class="flex-1 min-w-0">
    <div class="mobile-tabs hidden max-[900px]:flex max-[900px]:gap-[var(--sp-1)] max-[900px]:overflow-x-auto max-[900px]:pb-[6px] max-[900px]:mb-[var(--sp-3)]">
      {#each navItems as it}
        <button class={tab === it.key ? chipOn : chipOff} onclick={() => (tab = it.key)}>
          {it.label}
        </button>
      {/each}
    </div>

    {#if error}
      <div class="p-[var(--sp-3)] bg-danger-soft text-danger rounded-md u-fs-sm mb-[var(--sp-3)]">{error}</div>
    {/if}

    {#if tab === 'capabilities'}
      <CapabilitiesPanel onNavigate={(key) => (tab = key)} />
    {:else if tab === 'cost'}
    <div class="inline-flex gap-[3px] mb-[var(--sp-4)] p-[3px] bg-surface-2 rounded-md">
      {#each ranges as r}
        <button class={range === r.key ? filterOn : filterOff} onclick={() => (range = r.key)}>{r.label}</button>
      {/each}
    </div>
    <p class="[margin:0_0_var(--sp-3)] u-fs-xs text-muted leading-[1.6]">
      费用按每条调用发生时的价格计算：改了今天的价格，不会回溯改写上个月的花费。
      显示「未定价」时，去「模型服务」里补上对应模型的价格。
    </p>

    {#if report}
      <div class="grid grid-cols-[repeat(auto-fit,minmax(7rem,1fr))] gap-[var(--sp-3)] mb-[var(--sp-4)]">
        <div class="flex flex-col gap-[2px] py-[var(--sp-3)] px-[var(--sp-4)] bg-surface [border:1px_solid_var(--border)] rounded-md [box-shadow:var(--shadow-sm)]">
          <span class="u-fs-xs text-muted">费用</span>
          <span class="text-[length:1.15rem] font-semibold [font-variant-numeric:tabular-nums]">{formatCost(report.cost)}</span>
        </div>
        <div class="flex flex-col gap-[2px] py-[var(--sp-3)] px-[var(--sp-4)] bg-surface [border:1px_solid_var(--border)] rounded-md [box-shadow:var(--shadow-sm)]">
          <span class="u-fs-xs text-muted">调用</span>
          <span class="text-[length:1.15rem] font-semibold [font-variant-numeric:tabular-nums]">{report.calls}</span>
        </div>
        <div class="flex flex-col gap-[2px] py-[var(--sp-3)] px-[var(--sp-4)] bg-surface [border:1px_solid_var(--border)] rounded-md [box-shadow:var(--shadow-sm)]">
          <span class="u-fs-xs text-muted">输入</span>
          <span class="text-[length:1.15rem] font-semibold [font-variant-numeric:tabular-nums]">{shortTokens(report.input_tokens)}</span>
        </div>
        <div class="flex flex-col gap-[2px] py-[var(--sp-3)] px-[var(--sp-4)] bg-surface [border:1px_solid_var(--border)] rounded-md [box-shadow:var(--shadow-sm)]">
          <span class="u-fs-xs text-muted">输出</span>
          <span class="text-[length:1.15rem] font-semibold [font-variant-numeric:tabular-nums]">{shortTokens(report.output_tokens)}</span>
        </div>
        {#if report.cached_tokens}
          <div class="flex flex-col gap-[2px] py-[var(--sp-3)] px-[var(--sp-4)] bg-surface [border:1px_solid_var(--border)] rounded-md [box-shadow:var(--shadow-sm)]">
            <span class="u-fs-xs text-muted">缓存命中</span>
            <span class="text-[length:1.15rem] font-semibold [font-variant-numeric:tabular-nums]">{shortTokens(report.cached_tokens)}</span>
          </div>
        {/if}
      </div>

      {#if report.by_model?.length}
        <!-- 迷你条形：一眼看出钱花在哪个模型上。
             未定价的按调用次数占比，避免被当成 0 排到最矮。 -->
        <div class="flex flex-col gap-[6px] mb-[var(--sp-4)] py-[var(--sp-3)] px-[var(--sp-4)] bg-surface [border:1px_solid_var(--border)] rounded-md" aria-label="按模型的费用占比">
          {#each report.by_model as m}
            <div class="flex items-center gap-[var(--sp-3)] u-fs-xs">
              <span class="flex-[0_0_10rem] flex items-baseline gap-[5px] overflow-hidden whitespace-nowrap text-ellipsis">
                <span class="text-muted mr-[6px]">{m.provider}</span>
                <span class="font-mono text-[length:0.9em]">{m.model}</span>
              </span>
              <div class="flex-1 h-[8px] bg-surface-2 rounded-full overflow-hidden">
                <div
                  class="h-full rounded-full [transition:width_0.3s_ease] {m.priced ? 'bg-accent' : 'bg-faint'}"
                  style={`width: ${barWidth(m, report)}%`}
                  title={`${formatCost(m.cost, m.priced)} · ${m.calls} 次调用`}
                ></div>
              </div>
              <span class="flex-[0_0_4.5rem] text-right text-fg-secondary [font-variant-numeric:tabular-nums]">{formatCost(m.cost, m.priced)}</span>
            </div>
          {/each}
        </div>

        <table class="w-full border-collapse u-fs-sm">
          <thead>
            <tr>
              <th class={thL}>模型</th>
              <th class={thR}>调用</th>
              <th class={thR}>输入</th>
              <th class={thR}>输出</th>
              <th class={thR}>费用</th>
            </tr>
          </thead>
          <tbody>
            {#each report.by_model as m}
              <tr>
                <td class={tdL}>
                  <span class="text-muted mr-[6px]">{m.provider}</span>
                  <span class="font-mono text-[length:0.9em]">{m.model}</span>
                </td>
                <td class={tdR}>{m.calls}</td>
                <td class={tdR}>{shortTokens(m.input_tokens)}</td>
                <td class={tdR}>{shortTokens(m.output_tokens)}</td>
                <td class={tdR}>{formatCost(m.cost, m.priced)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else if report.no_session}
        <p class="text-muted u-fs-sm text-center p-[var(--sp-5)] flex items-center justify-center gap-[6px]">先在左侧选一个会话，才能看它的花费。</p>
      {:else if !loading}
        <p class="text-muted u-fs-sm text-center p-[var(--sp-5)] flex items-center justify-center gap-[6px]">
          这段时间还没有调用记录。
          {#if range === 'today'}
            发一条消息试试，这里会出现它的 token 用量与花费。
            <br />
            <span class="text-faint">花费为 0 的话，去「模型服务」里补上各模型的价格。</span>
          {/if}
        </p>
      {/if}
    {/if}
  {:else if tab === 'providers'}
    {#if providers}
      <!-- {#key} 让保存成功后重挂载编辑器，草稿自然重置为最新数据 -->
      {#key providers}
        <ProviderEditor {providers} onSaved={(fresh) => (providers = fresh)} />
      {/key}
    {/if}
  {:else if tab === 'context'}
    <ContextInspector />
  {:else if tab === 'skills'}
    <SkillsPanel />
  {:else if tab === 'mcp'}
    <MCPPanel />
  {:else if tab === 'data'}
    <DataPanel />
  {:else if tab === 'about'}
    <AboutPanel />
  {:else if tab === 'memory'}
    <MemoryPanel />
  {:else if tab === 'kb'}
    <KnowledgePanel focusDoc={citeTarget} />
  {:else if tab === 'security'}
    <SecurityPanel status={auth} onChanged={loadAuth} />
  {/if}

  {#if loading && (tab === 'cost' || tab === 'providers' || tab === 'security')}
    <p class="text-muted u-fs-sm text-center p-[var(--sp-5)] flex items-center justify-center gap-[6px]"><Icon name="clock" size={14} /> 加载中…</p>
  {/if}
  </div>
</div>


<style>
  /* 选中项的底色要压过 hover 原子类。 */
  .nav-item.active {
    background: var(--accent-soft);
    color: var(--accent);
    font-weight: 600;
  }
  /* 全局 * { scrollbar-width: thin } 未分层，原子类压不过；webkit 伪元素也没有对应原子类。 */
  .mobile-tabs {
    scrollbar-width: none;
  }
  .mobile-tabs::-webkit-scrollbar {
    display: none;
  }
</style>

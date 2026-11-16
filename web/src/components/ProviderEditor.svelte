<script>
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';
  import { humanize } from '../lib/errors.js';

  let { providers, onSaved = null } = $props();

  // 唯一的可编辑状态。父组件的 providers 是权威数据，草稿只在
  // 用户改动期间存在；保存成功后父组件通过 onSaved 更新 prop，
  // 外层用 {#key} 重挂载本组件来重置草稿。
  //
  // 只读一次初始值是刻意的：改成 $derived 会让父组件每次刷新
  // 都覆盖用户正在编辑的内容。重挂载才是重置的正确时机。
  // svelte-ignore state_referenced_locally
  let draft = $state(toDraft(providers));
  // svelte-ignore state_referenced_locally
  let editedRoutes = $state(toRoutes(providers));
  let busy = $state(false);
  let error = $state('');
  let saved = $state(false);

  // 远端模型列表只做输入提示：点「获取」才查询，结果按 provider 名缓存。
  // 不自动写入配置——上下文窗口、是否支持工具这些只有用户知道。
  let remoteModels = $state({});
  let fetching = $state('');
  let fetchError = $state({});

  // 加密存储的状态：不可用时就不要展示「直接填写密钥」，
  // 免得用户填了却存不下来。
  let secrets = $state(null);

  $effect(() => {
    api
      .secretsStatus()
      .then((s) => (secrets = s))
      .catch(() => (secrets = { available: false }));
  });

  // 重置会清空所有密钥，必须二次确认
  let resetting = $state(false);
  let resetError = $state('');

  async function resetKeys() {
    if (!confirm('这会清空所有已保存的 API Key，需要重新填写。确定继续？')) {
      return;
    }
    resetting = true;
    resetError = '';
    try {
      await api.resetSecrets();
      // 密钥没了，界面上的「已保存」状态也要跟着清掉
      draft = draft.map((p) => ({ ...p, has_key: false, key_source: 'none' }));
    } catch (err) {
      resetError = err.message;
    } finally {
      resetting = false;
    }
  }

  async function fetchModels(name) {
    if (!name || fetching) return;
    fetching = name;
    fetchError = { ...fetchError, [name]: '' };
    try {
      const res = await api.providerModels(name);
      remoteModels = { ...remoteModels, [name]: (res.models || []).map((m) => m.id) };
    } catch (err) {
      // 连通性测试失败：说清是「连不上」还是「密钥不对」，
      // 而不是把 502 与底层文案原样丢出来
      fetchError = { ...fetchError, [name]: humanize(err) };
    } finally {
      fetching = '';
    }
  }

  function toDraft(src) {
    return (src?.providers || []).map((p) => ({
      name: p.name,
      kind: p.kind,
      base_url: p.base_url || '',
      api_key_env: p.api_key_env || '',
      has_key: !!p.has_key,
      key_source: p.key_source || 'none',
      // 三个字段只存在于草稿：api_key 是本次要写入的新密钥，
      // show_key 控制明文显示，clear_key 是显式清除意图。
      api_key: '',
      show_key: false,
      clear_key: false,
      timeout_s: p.timeout_s || 120,
      models: (p.models || []).map((m) => ({ ...m })),
    }));
  }

  function toRoutes(src) {
    return Object.entries(src?.routes || {}).map(([task, cands]) => ({
      task,
      candidates: [...cands],
    }));
  }

  function addProvider() {
    draft = [
      ...draft,
      {
        name: 'new-provider',
        kind: 'openai-compat',
        base_url: '',
        api_key_env: '',
        has_key: false,
        key_source: 'none',
        api_key: '',
        show_key: false,
        clear_key: false,
        timeout_s: 120,
        models: [],
      },
    ];
  }

  function removeProvider(i) {
    draft = draft.filter((_, idx) => idx !== i);
  }

  function addModel(pi) {
    draft[pi].models = [
      ...draft[pi].models,
      {
        id: '',
        context_window: 128000,
        max_output: 8192,
        tools: true,
        price_in: 0,
        price_out: 0,
        price_cached: 0,
      },
    ];
  }

  function removeModel(pi, mi) {
    draft[pi].models = draft[pi].models.filter((_, idx) => idx !== mi);
  }

  async function save() {
    busy = true;
    error = '';
    saved = false;
    try {
      const payload = {
        providers: draft.map((p) => ({
          name: p.name.trim(),
          kind: p.kind,
          base_url: p.base_url.trim(),
          api_key_env: p.api_key_env.trim(),
          // 只在用户真的输入了新密钥时发送；留空 = 保留原值
          api_key: p.api_key,
          clear_key: p.clear_key,
          timeout_s: Number(p.timeout_s) || 120,
          models: p.models.map((m) => ({
            id: m.id.trim(),
            context_window: Number(m.context_window) || 0,
            max_output: Number(m.max_output) || 0,
            tools: !!m.tools,
            vision: !!m.vision,
            thinking: !!m.thinking,
            prefix_cache: !!m.prefix_cache,
            price_in: Number(m.price_in) || 0,
            price_out: Number(m.price_out) || 0,
            price_cached: Number(m.price_cached) || 0,
          })),
        })),
        routes: Object.fromEntries(
          editedRoutes.map((r) => [r.task, r.candidates.map((c) => c.trim()).filter(Boolean)])
        ),
      };
      const fresh = await api.saveProviders(payload);
      // 交给父组件更新 prop；外层 {#key} 会重挂载本组件、重置草稿
      saved = true;
      setTimeout(() => (saved = false), 2500);
      onSaved?.(fresh);
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }

  const field =
    'min-w-0 py-[6px] px-[var(--sp-2)] [border:1px_solid_var(--border-strong)] rounded-sm [background:var(--bg)] text-fg [outline:none] u-fs-sm focus:border-accent';
  const fieldId =
    'flex-1 min-w-[10rem] py-[6px] px-[var(--sp-2)] [border:1px_solid_var(--border-strong)] rounded-sm [background:var(--bg)] text-fg [outline:none] u-fs-sm focus:border-accent font-mono';
  const fieldXs =
    'min-w-0 flex-1 py-[6px] px-[var(--sp-2)] [border:1px_solid_var(--border-strong)] rounded-sm [background:var(--bg)] text-fg [outline:none] u-fs-xs focus:border-accent font-mono';
  const ghost =
    'inline-flex items-center gap-[5px] py-[6px] px-[12px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--border)] bg-surface text-fg hover:bg-surface-hover';
  const ghostSm =
    'inline-flex items-center self-start gap-[5px] py-[3px] px-[9px] rounded-md cursor-pointer u-fs-xs [border:1px_solid_var(--border)] bg-surface text-fg hover:bg-surface-hover';
  const primaryBtn =
    'inline-flex items-center gap-[5px] py-[6px] px-[12px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--accent)] bg-accent text-accent-fg enabled:hover:bg-accent-hover disabled:opacity-50 disabled:cursor-default';
  const iconBtn =
    '[background:none] [border:none] p-[4px] rounded-sm cursor-pointer text-muted grid place-items-center hover:bg-surface-2 hover:text-danger';
  const codeChip = 'font-mono bg-surface-2 py-px px-[4px] rounded-[3px]';
  const note = 'mt-[var(--sp-2)] mb-0 u-fs-xs text-muted';
  const badge = {
    ok: 'py-px px-[7px] rounded-full text-[0.7rem] leading-[1.5] [background:color-mix(in_srgb,var(--success)_15%,transparent)] text-success',
    env: 'py-px px-[7px] rounded-full text-[0.7rem] leading-[1.5] bg-surface-2 text-fg-secondary',
    warn: 'py-px px-[7px] rounded-full text-[0.7rem] leading-[1.5] [background:color-mix(in_srgb,var(--warning)_18%,transparent)] text-warning',
    none: 'py-px px-[7px] rounded-full text-[0.7rem] leading-[1.5] bg-surface-2 text-muted',
  };
</script>

  <div class="flex flex-col gap-[var(--sp-3)]">
    <div class="flex items-center justify-between gap-[var(--sp-2)]">
      <button class={ghost} onclick={addProvider}>
        <Icon name="plus" size={14} /> 添加 Provider
      </button>
      <div class="flex items-center gap-[var(--sp-3)]">
        {#if saved}<span class="inline-flex items-center gap-[4px] text-success u-fs-xs"><Icon name="check" size={14} /> 已保存并生效</span>{/if}
        <button class={primaryBtn} onclick={save} disabled={busy}>
          {busy ? '保存中…' : '保存'}
        </button>
      </div>
    </div>

    {#if error}
      <div class="flex items-center gap-[6px] py-[var(--sp-2)] px-[var(--sp-3)] bg-danger-soft text-danger rounded-sm u-fs-sm"><Icon name="alert" size={14} /> {error}</div>
    {/if}

    {#if secrets && secrets.note}
      <p class="m-0 u-fs-xs text-muted leading-[1.6]">{secrets.note}</p>
    {/if}

    {#if secrets && secrets.available && draft.some((p) => p.has_key)}
      <details class="py-[var(--sp-2)] px-[var(--sp-3)] [border:1px_solid_var(--border)] rounded-md u-fs-xs text-muted">
        <summary class="cursor-pointer text-fg-secondary">密钥解不开了？</summary>
        <p class={note}>
          换了系统用户、换了机器，或数据目录里的密钥文件被替换后，已存的密钥
          都无法解密。重置会清空全部密钥，需要逐个重新填写。
        </p>
        <button class="mt-[var(--sp-2)] py-[5px] px-[12px] rounded-sm [border:1px_solid_var(--danger)] [background:transparent] text-danger cursor-pointer u-fs-xs enabled:hover:bg-danger-soft disabled:opacity-50 disabled:cursor-default" onclick={resetKeys} disabled={resetting}>
          {resetting ? '重置中…' : '清空全部密钥并重置'}
        </button>
        {#if resetError}
          <div class="flex items-center gap-[6px] py-[var(--sp-2)] px-[var(--sp-3)] bg-danger-soft text-danger rounded-sm u-fs-sm"><Icon name="alert" size={14} /> {resetError}</div>
        {/if}
      </details>
    {/if}

    {#each draft as p, pi}
      <section class="py-[var(--sp-3)] px-[var(--sp-4)] [border:1px_solid_var(--border)] rounded-md bg-surface">
        <header class="flex items-center gap-[var(--sp-2)] mb-[var(--sp-3)]">
          <input class="{field} flex-1 font-semibold" bind:value={p.name} placeholder="provider 名称" />
          <select class={field} bind:value={p.kind}>
            <option value="openai-compat">openai-compat</option>
            <option value="responses">responses</option>
            <option value="anthropic">anthropic</option>
          </select>
          <button class={iconBtn} onclick={() => removeProvider(pi)} title="删除 Provider">
            <Icon name="trash" size={14} />
          </button>
        </header>

        <div class="flex flex-wrap gap-[var(--sp-3)]">
          <label class="flex flex-col gap-[3px] flex-1 min-w-[10rem] u-fs-xs text-muted">
            <span>Base URL</span>
            <input class={field} bind:value={p.base_url} placeholder="https://api.example.com/v1" />
          </label>
          <label class="flex flex-col gap-[3px] flex-[0_0_7rem] min-w-[7rem] u-fs-xs text-muted">
            <span>超时（秒）</span>
            <input class={field} type="number" bind:value={p.timeout_s} min="1" />
          </label>
        </div>

        <div class="mt-[var(--sp-3)] flex flex-col gap-[6px]">
          <div class="flex items-center gap-[var(--sp-2)] u-fs-xs text-muted">
            <span class="font-semibold text-fg-secondary">API Key</span>
            {#if p.key_source === 'env'}
              <span class={badge.env}>来自环境变量</span>
            {:else if p.clear_key}
              <span class={badge.warn}>保存后清除</span>
            {:else if p.api_key}
              <span class={badge.ok}>保存后更新</span>
            {:else if p.has_key}
              <span class={badge.ok}>已保存（不会回显）</span>
            {:else}
              <span class={badge.none}>未设置</span>
            {/if}
          </div>

          {#if secrets && !secrets.available}
            <p class="mt-[var(--sp-2)] mb-0 u-fs-xs text-warning">
              加密存储不可用，密钥只能通过环境变量提供：在下面填变量名，
              真实值由启动进程的环境给出。
            </p>
            <input class={field} bind:value={p.api_key_env} placeholder="MY_API_KEY" />
          {:else}
            <div class="flex items-center gap-[var(--sp-2)]">
              <input
                class="{field} flex-1 font-mono"
                type={p.show_key ? 'text' : 'password'}
                bind:value={p.api_key}
                placeholder={p.has_key ? '留空表示不修改' : 'sk-...'}
                autocomplete="off"
                spellcheck="false"
                oninput={() => {
                  // 用户开始输入新密钥，就撤销「清除」意图，
                  // 否则两个意图会打架
                  if (p.api_key) p.clear_key = false;
                }}
              />
              <button
                class={ghostSm}
                type="button"
                onclick={() => (p.show_key = !p.show_key)}
              >
                {p.show_key ? '隐藏' : '显示'}
              </button>
              {#if p.has_key && !p.api_key}
                <button
                  class={ghostSm}
                  type="button"
                  onclick={() => (p.clear_key = true)}
                  title="保存后删除已存的密钥"
                >
                  清除
                </button>
              {/if}
            </div>
            <p class={note}>
              密钥加密后存在本地数据库里，不会写进配置文件，也不会回显。
              {#if p.api_key_env}
                当前还声明了环境变量 <code class={codeChip}>{p.api_key_env}</code>，它优先于这里保存的密钥。
              {/if}
            </p>
            <details class="mt-[var(--sp-2)] u-fs-xs text-muted">
              <summary class="cursor-pointer text-fg-secondary">改用环境变量提供密钥</summary>
              <div class="flex items-center gap-[var(--sp-2)] mt-[6px]">
                <input class="{field} flex-1" bind:value={p.api_key_env} placeholder="MY_API_KEY" />
                <button
                  class={ghostSm}
                  type="button"
                  onclick={() => {
                    p.api_key_env = '';
                  }}
                  disabled={!p.api_key_env}
                >
                  清除变量名
                </button>
              </div>
              <p class={note}>
                填了变量名后，加密存储里的密钥不再生效（两者不能同时用）。
              </p>
            </details>
          {/if}
        </div>

        <div class="mt-[var(--sp-3)] flex flex-col gap-[6px]">
          {#each p.models as m, mi}
            <div class="flex flex-col gap-[4px] py-[6px] {mi === 0 ? '' : '[border-top:1px_solid_var(--border)]'}">
              <div class="flex items-center gap-[var(--sp-2)] flex-wrap">
                <input
                  class={fieldId}
                  bind:value={m.id}
                  placeholder="模型 ID"
                  list={`models-${pi}`}
                />
                <input class="{field} w-[7rem]" type="number" bind:value={m.context_window} placeholder="上下文" />
                <input class="{field} w-[7rem]" type="number" bind:value={m.max_output} placeholder="最大输出" />
                <label class="inline-flex items-center gap-[4px] u-fs-xs text-fg-secondary whitespace-nowrap"><input class={field} type="checkbox" bind:checked={m.tools} /> 工具</label>
                <label class="inline-flex items-center gap-[4px] u-fs-xs text-fg-secondary whitespace-nowrap"><input class={field} type="checkbox" bind:checked={m.vision} /> 视觉</label>
                <label class="inline-flex items-center gap-[4px] u-fs-xs text-fg-secondary whitespace-nowrap"><input class={field} type="checkbox" bind:checked={m.thinking} /> 思考</label>
                <button class={iconBtn} onclick={() => removeModel(pi, mi)} title="删除模型">
                  <Icon name="close" size={13} />
                </button>
              </div>
              <div class="flex items-center gap-[var(--sp-3)] flex-wrap pl-[2rem]">
                <label class="inline-flex items-center gap-[4px] u-fs-xs text-muted">
                  <span>输入价</span>
                  <input class="{field} w-[5.5rem]" type="number" step="0.01" bind:value={m.price_in} placeholder="0" />
                </label>
                <label class="inline-flex items-center gap-[4px] u-fs-xs text-muted">
                  <span>输出价</span>
                  <input class="{field} w-[5.5rem]" type="number" step="0.01" bind:value={m.price_out} placeholder="0" />
                </label>
                <label class="inline-flex items-center gap-[4px] u-fs-xs text-muted">
                  <span>缓存命中价</span>
                  <input class="{field} w-[5.5rem]" type="number" step="0.01" bind:value={m.price_cached} placeholder="0" />
                </label>
                <span class="text-[0.7rem] text-faint">元 / 百万 token</span>
              </div>
            </div>
          {/each}
          <datalist id={`models-${pi}`}>
            {#each remoteModels[p.name] || [] as id}
              <option value={id}></option>
            {/each}
          </datalist>

          <div class="flex items-center gap-[var(--sp-2)] flex-wrap">
            <button class={ghostSm} onclick={() => addModel(pi)}>
              <Icon name="plus" size={13} /> 添加模型
            </button>
            <button
              class={ghostSm}
              onclick={() => fetchModels(p.name)}
              disabled={fetching === p.name}
              title="向服务端查询可用模型（不会自动改配置）"
            >
              <Icon name="search" size={13} />
              {fetching === p.name ? '查询中…' : '获取模型列表'}
            </button>
            {#if fetchError[p.name]}
              <span class="u-fs-xs text-danger">{fetchError[p.name]}</span>
            {:else if remoteModels[p.name]?.length}
              <span class="u-fs-xs text-muted">{remoteModels[p.name].length} 个可选</span>
            {/if}
          </div>
        </div>
      </section>
    {/each}

    <section class="py-[var(--sp-3)] px-[var(--sp-4)] [border:1px_solid_var(--border)] rounded-md">
      <h3 class="m-0 mb-[var(--sp-2)] u-fs-sm font-semibold">任务路由</h3>
      <p class={note}>每行一个候选，按优先级从上到下；格式 <code class={codeChip}>provider/model-id</code>。</p>
      {#each editedRoutes as r}
        <label class="mt-[var(--sp-2)] flex flex-row items-center gap-[var(--sp-3)] flex-1 u-fs-xs text-muted">
          <span class="flex-[0_0_6rem] font-mono u-fs-xs text-fg-secondary">{r.task}</span>
          <input
            class={fieldXs}
            value={r.candidates.join(', ')}
            oninput={(e) => {
              r.candidates = e.currentTarget.value.split(',').map((s) => s.trim());
            }}
            placeholder="provider/model"
          />
        </label>
      {/each}
    </section>
  </div>

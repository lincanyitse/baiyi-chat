<script>
  // 首次配置向导：零 Provider 时替代随便聊的欢迎页。
  //
  // 只问最少的三件事：协议、地址、密钥。模型 ID 用服务端返回的列表
  // 让用户选——手抄模型 ID 是首次配置最容易出错的一步。
  // 高级配置（上下文窗口、价格、路由）留给设置页。
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';
  import { humanize } from '../lib/errors.js';

  let { onDone = null } = $props();

  let step = $state(1);
  let kind = $state('openai-compat');
  let baseUrl = $state('');
  let apiKey = $state('');
  let providerName = $state('');
  let models = $state([]); // 服务端返回的候选
  let chosenModel = $state('');
  let modelsLoading = $state(false);
  let modelsError = $state('');
  let busy = $state(false);
  let error = $state('');

  const choice =
    'choice flex flex-col gap-[2px] text-left py-[var(--sp-2)] px-[var(--sp-3)] bg-surface [border:1px_solid_var(--border)] rounded-md cursor-pointer hover:bg-surface-hover hover:border-border-strong';
  const choiceCompact =
    'choice flex flex-col gap-[2px] text-left py-[5px] px-[10px] bg-surface [border:1px_solid_var(--border)] rounded-md cursor-pointer hover:bg-surface-hover hover:border-border-strong';
  const field =
    'py-[8px] px-[var(--sp-2)] [border:1px_solid_var(--border-strong)] rounded-sm [background:var(--bg)] text-fg [outline:none] u-fs-sm focus:border-accent';
  const labelCls = 'flex flex-col gap-[4px] u-fs-xs text-muted';
  const ghost =
    'py-[7px] px-[16px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--border)] bg-surface text-fg hover:bg-surface-hover';
  const primaryBtn =
    'py-[7px] px-[16px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--accent)] bg-accent text-accent-fg enabled:hover:bg-accent-hover disabled:opacity-50 disabled:cursor-default';
  const stepOn = 'text-accent font-semibold';
  const stepDone = 'text-fg-secondary';

  const kinds = [
    { value: 'openai-compat', label: 'OpenAI 兼容', hint: 'OpenAI、DeepSeek、Moonshot、自建网关等' },
    { value: 'responses', label: 'OpenAI Responses', hint: 'OpenAI 新的 Responses API' },
    { value: 'anthropic', label: 'Anthropic', hint: 'Claude 系列' },
  ];

  function next() {
    if (step === 1) {
      step = 2;
      return;
    }
    if (step === 2) {
      if (!baseUrl.trim()) {
        error = '请填写服务地址';
        return;
      }
      if (!providerName.trim()) {
        providerName = deriveName();
      }
      step = 3;
      loadModels();
    }
  }

  function deriveName() {
    try {
      const u = new URL(baseUrl.trim());
      return u.hostname.replace(/[^a-zA-Z0-9.-]/g, '-').split('.')[0] || 'provider';
    } catch {
      return 'provider';
    }
  }

  async function loadModels() {
    modelsLoading = true;
    modelsError = '';
    models = [];
    try {
      // 先把配置存下来，服务端才能用它去查模型列表。
      // 这一步没有模型，路由也是空的——保存不会失败，因为
      // Provider 定义本身是合法的。
      await saveProvider(null);
      const res = await api.providerModels(providerName.trim());
      models = res.models || [];
      if (models.length === 1) chosenModel = models[0].id;
    } catch (err) {
      modelsError = humanize(err);
    } finally {
      modelsLoading = false;
    }
  }

  async function saveProvider(modelId) {
    const p = {
      name: providerName.trim(),
      kind,
      base_url: baseUrl.trim(),
      api_key: apiKey,
      timeout_s: 120,
      models: modelId
        ? [{ id: modelId, context_window: 128000, max_output: 8192, tools: true }]
        : [],
    };
    await api.saveProviders({
      providers: [p],
      routes: modelId ? { chat: [`${p.name}/${modelId}`] } : {},
    });
  }

  async function finish() {
    if (!chosenModel) {
      error = '请选择一个模型';
      return;
    }
    busy = true;
    error = '';
    try {
      await saveProvider(chosenModel);
      onDone?.();
    } catch (err) {
      error = humanize(err);
    } finally {
      busy = false;
    }
  }
</script>

<div class="flex-[1] flex flex-col justify-center max-w-[32rem] [margin:0_auto] p-[var(--sp-5)] w-full gap-[var(--sp-3)]">
  <header class="text-center mb-[var(--sp-2)]">
    <div class="w-[48px] h-[48px] [margin:0_auto_var(--sp-3)] rounded-lg bg-accent text-accent-fg grid place-items-center text-[length:1.4rem] font-semibold">白</div>
    <h1 class="[margin:0_0_var(--sp-2)] text-[length:1.35rem] font-semibold">配置模型服务</h1>
    <p class="m-0 text-muted u-fs-sm">白泽自己不产生回答——它调用你配置的模型服务。</p>
  </header>

  <ol class="flex justify-center gap-[var(--sp-4)] [margin:var(--sp-2)_0] p-0 [list-style:none] u-fs-xs text-faint">
    <li class={step === 1 ? stepOn : step > 1 ? stepDone : ''}>选协议</li>
    <li class={step === 2 ? stepOn : step > 2 ? stepDone : ''}>填地址与密钥</li>
    <li class={step === 3 ? stepOn : ''}>选模型</li>
  </ol>

  {#if step === 1}
    <div class="flex flex-col gap-[6px]">
      {#each kinds as k}
        <button
          class={choice}
          class:selected={kind === k.value}
          onclick={() => {
            kind = k.value;
            next();
          }}
        >
          <span class="u-fs-sm text-fg">{k.label}</span>
          <span class="u-fs-xs text-muted">{k.hint}</span>
        </button>
      {/each}
    </div>
  {:else if step === 2}
    <div class="flex flex-col gap-[var(--sp-3)]">
      <label class={labelCls}>
        <span>服务地址</span>
        <input class={field} bind:value={baseUrl} placeholder="https://api.example.com/v1" />
      </label>
      <label class={labelCls}>
        <span>API Key</span>
        <input
          class={field}
          type="password"
          bind:value={apiKey}
          placeholder="sk-..."
          autocomplete="off"
          spellcheck="false"
        />
      </label>
      <p class="m-0 u-fs-xs text-muted leading-[1.6]">
        密钥加密后存在本机数据库，不会写进配置文件，也不会回显。
        也可以留空、之后再在设置里用环境变量提供。
      </p>
      <label class={labelCls}>
        <span>名称（可选）</span>
        <input class={field} bind:value={providerName} placeholder={deriveName()} />
      </label>
    </div>
    <div class="flex justify-between gap-[var(--sp-2)] mt-[var(--sp-2)]">
      <button class={ghost} onclick={() => (step = 1)}>上一步</button>
      <button class={primaryBtn} onclick={next}>下一步</button>
    </div>
  {:else}
    <div class="flex flex-col gap-[var(--sp-3)]">
      <p class="m-0 u-fs-xs text-muted leading-[1.6]">已按地址与密钥向服务端查询可用模型。</p>
      {#if modelsLoading}
        <p class="flex items-center gap-[6px] m-0 u-fs-sm text-muted"><Icon name="clock" size={14} /> 查询中…</p>
      {:else if models.length}
        <div class="max-h-[14rem] overflow-y-auto flex flex-col gap-[4px]">
          {#each models as m}
            <button
              class={choiceCompact}
              class:selected={chosenModel === m.id}
              onclick={() => (chosenModel = m.id)}
            >
              <span class="u-fs-sm text-fg font-mono">{m.id}</span>
            </button>
          {/each}
        </div>
      {:else}
        <p class="m-0 u-fs-sm text-danger">
          {modelsError || '服务端没有返回模型列表。'}
        </p>
        <label class={labelCls}>
          <span>手动填写模型 ID</span>
          <input class={field} bind:value={chosenModel} placeholder="gpt-4o-mini" />
        </label>
      {/if}
    </div>
    <div class="flex justify-between gap-[var(--sp-2)] mt-[var(--sp-2)]">
      <button class={ghost} onclick={() => (step = 2)}>上一步</button>
      <button class={primaryBtn} onclick={finish} disabled={busy}>
        {busy ? '保存中…' : '完成'}
      </button>
    </div>
  {/if}

  {#if error}
    <div class="flex items-center gap-[6px] py-[var(--sp-2)] px-[var(--sp-3)] bg-danger-soft text-danger rounded-sm u-fs-sm"><Icon name="alert" size={14} /> {error}</div>
  {/if}
</div>

<style>
  /* 选中要压过 hover，单条工具类比不过去 */
  .choice.selected {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 8%, var(--surface));
  }
</style>

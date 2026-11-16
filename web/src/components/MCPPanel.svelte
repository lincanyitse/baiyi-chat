<script>
  // 插件（MCP server）管理。
  //
  // MCP server 是外部进程或 HTTP 服务，能执行任意操作——界面上必须
  // 把「会执行本机命令」说清楚，并对写操作要求显式声明风险级别。
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';
  import { humanize } from '../lib/errors.js';

  let servers = $state(null); // MCPDTO[]
  let loading = $state(true);
  let error = $state('');
  let saved = $state(false);
  let busy = $state(false);
  // alias → 保存后的结果消息
  let results = $state({});
  let reconnecting = $state('');

  async function load() {
    loading = true;
    error = '';
    try {
      const res = await api.mcp();
      servers = (res.servers || []).map(toDraft);
    } catch (err) {
      error = humanize(err);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });

  function toDraft(s) {
    return {
      alias: s.alias,
      transport: s.transport || 'stdio',
      command: s.command || '',
      // 参数用空格分隔编辑：界面里做成数组输入框太重，
      // 而真实的 args 很少含空格（含空格的值用户可以用引号自己处理）
      argsText: (s.args || []).join(' '),
      url: s.url || '',
      allowedToolsText: (s.allowed_tools || []).join(', '),
      risk: s.risk || 'read',
      enabled: s.enabled !== false,
      // 运行时状态，只读
      connected: !!s.connected,
      lastError: s.last_error || '',
      tools: s.tools || [],
    };
  }

  function addServer() {
    servers = [
      ...servers,
      {
        alias: '',
        transport: 'stdio',
        command: '',
        argsText: '',
        url: '',
        allowedToolsText: '',
        risk: 'read',
        enabled: true,
        connected: false,
        lastError: '',
        tools: [],
      },
    ];
  }

  function removeServer(i) {
    servers = servers.filter((_, idx) => idx !== i);
  }

  function splitArgs(s) {
    return (s || '')
      .split(/\s+/)
      .map((x) => x.trim())
      .filter(Boolean);
  }

  function splitTools(s) {
    return (s || '')
      .split(',')
      .map((x) => x.trim())
      .filter(Boolean);
  }

  async function save() {
    busy = true;
    error = '';
    saved = false;
    results = {};
    try {
      const payload = servers.map((s) => ({
        alias: s.alias.trim(),
        transport: s.transport,
        command: s.command.trim(),
        args: splitArgs(s.argsText),
        url: s.url.trim(),
        allowed_tools: splitTools(s.allowedToolsText),
        risk: s.risk,
        enabled: s.enabled,
      }));
      const res = await api.saveMCP(payload);

      // 逐项结果：一个连不上不该让整次保存失败
      const byAlias = {};
      for (const r of res.results || []) {
        byAlias[r.alias] = r;
      }
      results = byAlias;

      saved = true;
      setTimeout(() => (saved = false), 2500);

      // 重新拉一次拿最新的连接状态与工具列表
      await load();
    } catch (err) {
      error = humanize(err);
    } finally {
      busy = false;
    }
  }

  async function reconnect(alias) {
    if (reconnecting) return;
    reconnecting = alias;
    error = '';
    try {
      await api.reconnectMCP(alias);
      await load();
    } catch (err) {
      results = { ...results, [alias]: { alias, ok: false, message: humanize(err) } };
    } finally {
      reconnecting = '';
    }
  }
  const field =
    'min-w-0 py-[6px] px-[var(--sp-2)] [border:1px_solid_var(--border-strong)] rounded-sm [background:var(--bg)] text-fg [outline:none] u-fs-sm focus:border-accent';
  const monoField =
    'min-w-0 py-[6px] px-[var(--sp-2)] [border:1px_solid_var(--border-strong)] rounded-sm [background:var(--bg)] text-fg [outline:none] u-fs-sm focus:border-accent font-mono';
  const aliasField =
    'flex-[1] min-w-[8rem] py-[6px] px-[var(--sp-2)] [border:1px_solid_var(--border-strong)] rounded-sm [background:var(--bg)] text-fg [outline:none] u-fs-sm focus:border-accent font-mono font-semibold';
  const labelCls = 'flex flex-col gap-[3px] flex-[1] min-w-[8rem] u-fs-xs text-muted';
  const wideCls = 'flex flex-col gap-[3px] flex-[2] min-w-[14rem] u-fs-xs text-muted';
  const ghost =
    'inline-flex items-center gap-[5px] whitespace-nowrap py-[6px] px-[12px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--border)] bg-surface text-fg enabled:hover:bg-surface-hover';
  const ghostSm =
    'inline-flex items-center gap-[5px] whitespace-nowrap ml-auto py-[2px] px-[8px] rounded-md cursor-pointer u-fs-xs [border:1px_solid_var(--border)] bg-surface text-fg enabled:hover:bg-surface-hover';
  const primaryBtn =
    'inline-flex items-center gap-[5px] whitespace-nowrap py-[6px] px-[12px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--accent)] bg-accent text-accent-fg enabled:hover:bg-accent-hover disabled:opacity-50 disabled:cursor-default';
  const iconBtn =
    'flex-none [background:none] [border:none] p-[4px] rounded-sm cursor-pointer text-muted grid place-items-center hover:bg-surface-2 hover:text-danger';
  const dot = {
    ok: 'w-[7px] h-[7px] [border-radius:50%] flex-none bg-success',
    bad: 'w-[7px] h-[7px] [border-radius:50%] flex-none bg-danger',
    idle: 'w-[7px] h-[7px] [border-radius:50%] flex-none bg-faint',
  };
</script>

<div class="flex flex-col gap-[var(--sp-3)]">
  <div class="flex items-start justify-between gap-[var(--sp-3)]">
    <p class="m-0 u-fs-xs text-muted leading-[1.7]">
      插件是 MCP server：外部进程（stdio）或 HTTP 服务。加上之后它的工具会直接出现在对话里。<br />
      <strong class="text-warning">stdio 会在本机执行命令</strong>——只添加你信任的 server。
    </p>
    <div class="flex gap-[var(--sp-2)] flex-none">
      <button class={ghost} onclick={addServer}><Icon name="plus" size={14} /> 添加</button>
      <button class={primaryBtn} onclick={save} disabled={busy || !servers}>
        {busy ? '保存中…' : '保存'}
      </button>
    </div>
  </div>

  {#if saved}
    <div class="flex items-center gap-[6px] py-[var(--sp-2)] px-[var(--sp-3)] [background:color-mix(in_srgb,var(--success)_12%,transparent)] text-success rounded-sm u-fs-sm"><Icon name="check" size={14} /> 已保存并生效</div>
  {/if}
  {#if error}
    <div class="flex items-center gap-[6px] py-[var(--sp-2)] px-[var(--sp-3)] bg-danger-soft text-danger rounded-sm u-fs-sm"><Icon name="alert" size={14} /> {error}</div>
  {/if}

  {#if loading && !servers}
    <p class="flex items-center gap-[6px] m-0 u-fs-sm text-muted"><Icon name="clock" size={14} /> 加载中…</p>
  {:else if servers && servers.length === 0}
    <div class="p-[var(--sp-5)] [border:1px_dashed_var(--border-strong)] rounded-md text-center">
      <p class="[margin:0_0_var(--sp-2)] u-fs-sm text-fg-secondary">还没有插件。</p>
      <p class="max-w-[34rem] [margin:0_auto] u-fs-sm text-fg-secondary leading-[1.7]">
        常见的入门选择：文件系统、数据库查询、浏览器自动化。每个 MCP server
        都有自己的安装方式，装好后把命令填到这里。
      </p>
    </div>
  {:else if servers}
    <div class="flex flex-col gap-[var(--sp-2)]">
      {#each servers as s, i}
        <section class="py-[var(--sp-3)] px-[var(--sp-4)] [border:1px_solid_var(--border)] rounded-md bg-surface {s.enabled ? '' : 'opacity-[0.62]'}">
          <header class="flex items-center gap-[var(--sp-2)] mb-[var(--sp-3)]">
            <input class={aliasField} bind:value={s.alias} placeholder="alias（短名，唯一）" />
            <select class={field} bind:value={s.transport}>
              <option value="stdio">stdio（本机命令）</option>
              <option value="http">http（远程地址）</option>
            </select>
            <label class="inline-flex items-center gap-[4px] u-fs-xs text-fg-secondary whitespace-nowrap cursor-pointer">
              <input class={field} type="checkbox" bind:checked={s.enabled} /> 启用
            </label>
            <button class={iconBtn} onclick={() => removeServer(i)} title="删除">
              <Icon name="trash" size={14} />
            </button>
          </header>

          {#if s.transport === 'stdio'}
            <div class="flex flex-wrap gap-[var(--sp-3)] mt-[var(--sp-2)]">
              <label class={wideCls}>
                <span>命令</span>
                <input class={monoField} bind:value={s.command} placeholder="npx" />
              </label>
              <label class={wideCls}>
                <span>参数（空格分隔）</span>
                <input
                  class={monoField}
                  bind:value={s.argsText}
                  placeholder="-y @modelcontextprotocol/server-filesystem D:/docs"
                />
              </label>
            </div>
            <p class="[margin:6px_0_0] u-fs-xs text-warning">
              保存后会立即执行这条命令。命令与参数由你负责——只填你信任的。
            </p>
          {:else}
            <div class="flex flex-wrap gap-[var(--sp-3)] mt-[var(--sp-2)]">
              <label class={wideCls}>
                <span>地址</span>
                <input class={monoField} bind:value={s.url} placeholder="http://127.0.0.1:3000/mcp" />
              </label>
            </div>
          {/if}

          <div class="flex flex-wrap gap-[var(--sp-3)] mt-[var(--sp-2)]">
            <label class={labelCls}>
              <span>风险级别</span>
              <select class={field} bind:value={s.risk}>
                <option value="read">只读（自动执行）</option>
                <option value="write">写入（可能需确认）</option>
                <option value="dangerous">危险（每次都要确认）</option>
              </select>
            </label>
            <label class={wideCls}>
              <span>允许的工具（逗号分隔，留空=全部）</span>
              <input class={monoField} bind:value={s.allowedToolsText} placeholder="read_file, list_directory" />
            </label>
          </div>

          <div class="flex items-center gap-[8px] mt-[var(--sp-3)] pt-[var(--sp-2)] [border-top:1px_solid_var(--border)] u-fs-xs flex-wrap">
            {#if s.connected}
              <span class={dot.ok}></span>
              <span class="text-fg-secondary">已连接</span>
              <span class="text-muted font-mono text-[length:0.7rem] overflow-hidden text-ellipsis whitespace-nowrap max-w-full">{s.tools.length} 个工具{#if s.tools.length}: {s.tools.join(', ')}{/if}</span>
            {:else if s.alias && s.enabled}
              <span class={dot.bad}></span>
              <span class="text-fg-secondary">未连接</span>
              {#if s.lastError}<span class="text-danger text-[length:0.7rem]">{s.lastError}</span>{/if}
            {:else if !s.enabled}
              <span class={dot.idle}></span>
              <span class="text-fg-secondary">已禁用</span>
            {:else}
              <span class={dot.idle}></span>
              <span class="text-fg-secondary">未保存</span>
            {/if}

            {#if s.alias && s.enabled}
              <button
                class={ghostSm}
                onclick={() => reconnect(s.alias)}
                disabled={reconnecting === s.alias}
              >
                {reconnecting === s.alias ? '重连中…' : '重连'}
              </button>
            {/if}
          </div>

          {#if results[s.alias] && !results[s.alias].ok}
            <p class="[margin:6px_0_0] u-fs-xs text-danger">{results[s.alias].message}</p>
          {/if}
        </section>
      {/each}
    </div>
  {/if}
</div>

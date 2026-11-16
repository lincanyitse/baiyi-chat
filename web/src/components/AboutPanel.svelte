<script>
  // 「关于」：版本、路径、健康状态、退出登录、诊断导出、重置数据。
  //
  // 这一页的定位是「不用翻文档也能自己解决」：出问题时用户先来这里，
  // 能看到版本、数据在哪、日志在哪，并一键导出诊断包。
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';
  import { humanize } from '../lib/errors.js';
  import { formatSize } from '../lib/format.js';
  import {
    readTheme, readFont, readAccent,
    setTheme, setFont, setAccent, accents,
  } from '../lib/appearance.js';

  let theme = $state(readTheme());
  let font = $state(readFont());
  let accent = $state(readAccent());

  const themes = [
    { key: 'system', label: '跟随系统' },
    { key: 'light', label: '浅色' },
    { key: 'dark', label: '深色' },
  ];
  const fonts = [
    { key: 'small', label: '小' },
    { key: 'normal', label: '标准' },
    { key: 'large', label: '大' },
  ];

  let info = $state(null);
  let health = $state(null);
  let loading = $state(true);
  let error = $state('');
  let copied = $state('');

  // 重置：先选范围，再要求把范围词敲一遍
  let resetScope = $state('');
  let resetConfirm = $state('');
  let resetting = $state(false);
  let resetResult = $state(null);

  async function load() {
    loading = true;
    error = '';
    try {
      info = await api.system();
      health = await api.health().catch(() => null);
    } catch (err) {
      error = humanize(err);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });

  async function copy(text, key) {
    try {
      await navigator.clipboard.writeText(text);
      copied = key;
      setTimeout(() => (copied = ''), 1800);
    } catch {
      error = '复制失败，请手动选中：' + text;
    }
  }

  async function logout() {
    try {
      await api.logout();
      location.reload();
    } catch (err) {
      error = humanize(err);
    }
  }

  async function doReset() {
    if (!resetScope || resetting) return;
    if (resetConfirm !== resetScope) {
      error = '确认文本不匹配，请输入 ' + resetScope;
      return;
    }
    resetting = true;
    error = '';
    resetResult = null;
    try {
      resetResult = await api.resetData(resetScope, resetConfirm);
      resetScope = '';
      resetConfirm = '';
      await load();
    } catch (err) {
      error = humanize(err);
    } finally {
      resetting = false;
    }
  }

  function uptime(sec) {
    if (!sec) return '—';
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    if (h > 0) return `${h} 小时 ${m} 分`;
    if (m > 0) return `${m} 分 ${sec % 60} 秒`;
    return `${sec} 秒`;
  }

  const h3 = '[margin:0_0_var(--sp-2)] u-fs-sm font-semibold';
  const block = 'py-[var(--sp-3)] px-[var(--sp-4)] [border:1px_solid_var(--border)] rounded-md bg-surface';
  const dangerBlock = 'py-[var(--sp-3)] px-[var(--sp-4)] [border:1px_solid_color-mix(in_srgb,var(--danger)_30%,var(--border))] rounded-md bg-surface';
  const note = '[margin:0_0_var(--sp-2)] u-fs-xs text-muted leading-[1.7]';
  const kv = 'grid grid-cols-[6rem_1fr] gap-x-[var(--sp-3)] gap-y-[6px] m-0 u-fs-xs';
  const dt = 'text-muted';
  const dd = 'm-0 text-fg-secondary [word-break:break-all]';
  const codeKv = 'font-mono text-[length:0.72rem]';
  // 后一个按钮只留左边线。不能在 [border:none] 上再叠 border-left，两条都会写 border，生成顺序不稳定。
  const segOffFirst = 'py-[5px] px-[var(--sp-3)] [border:none] bg-surface text-fg-secondary u-fs-xs cursor-pointer hover:bg-surface-hover';
  const segOffNext = 'py-[5px] px-[var(--sp-3)] [border-top:none] [border-right:none] [border-bottom:none] [border-left:1px_solid_var(--border)] bg-surface text-fg-secondary u-fs-xs cursor-pointer hover:bg-surface-hover';
  const segOnFirst = 'py-[5px] px-[var(--sp-3)] [border:none] bg-accent text-accent-fg u-fs-xs cursor-pointer';
  const segOnNext = 'py-[5px] px-[var(--sp-3)] [border-top:none] [border-right:none] [border-bottom:none] [border-left:1px_solid_var(--border)] bg-accent text-accent-fg u-fs-xs cursor-pointer';
  const swatchOff = 'w-[26px] h-[26px] [border-radius:50%] [border:2px_solid_transparent] [background:var(--swatch)] cursor-pointer grid place-items-center p-0 [box-shadow:var(--shadow-sm)] hover:[transform:scale(1.08)]';
  const swatchOn = 'w-[26px] h-[26px] [border-radius:50%] [border:2px_solid_var(--fg)] [background:var(--swatch)] cursor-pointer grid place-items-center p-0 [box-shadow:var(--shadow-sm)] hover:[transform:scale(1.08)]';
  const ghost = 'inline-flex items-center gap-[5px] py-[6px] px-[var(--sp-3)] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--border)] bg-surface text-fg no-underline hover:bg-surface-hover';
  const dangerBtn = 'inline-flex items-center gap-[5px] py-[6px] px-[var(--sp-3)] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--danger)] [background:transparent] text-danger no-underline enabled:hover:bg-danger-soft disabled:opacity-[0.45] disabled:cursor-default';
  const linkBtn = '[background:none] [border:none] text-accent cursor-pointer u-fs-xs px-[var(--sp-1)] py-0 hover:underline';
  const resetOff = 'flex items-start gap-[var(--sp-2)] py-[6px] px-[10px] [border:1px_solid_var(--border)] rounded-sm cursor-pointer u-fs-xs';
  const resetOn = 'flex items-start gap-[var(--sp-2)] py-[6px] px-[10px] [border:1px_solid_var(--danger)] [background:color-mix(in_srgb,var(--danger)_6%,transparent)] rounded-sm cursor-pointer u-fs-xs';
  const dotOk = 'flex items-center flex-wrap gap-[6px] mt-[var(--sp-2)] mr-0 mb-0 ml-0 w-[7px] h-[7px] [border-radius:50%] bg-success u-fs-xs text-success';
  const dotBad = 'inline-block w-[7px] h-[7px] [border-radius:50%] mr-[var(--sp-1)] bg-danger';
</script>

<div class="flex flex-col gap-[var(--sp-3)]">
  {#if error}
    <div class="flex items-center gap-[6px] py-[var(--sp-2)] px-[var(--sp-3)] bg-danger-soft text-danger rounded-sm u-fs-sm"><Icon name="alert" size={14} /> {error}</div>
  {/if}

  {#if loading && !info}
    <p class="flex items-center gap-[6px] m-0 u-fs-sm text-muted"><Icon name="clock" size={14} /> 加载中…</p>
  {:else if info}
    <section class={block}>
      <h3 class={h3}>外观</h3>
      <div class="flex items-center gap-[var(--sp-3)] mb-[var(--sp-2)]">
        <span class="flex-[0_0_3rem] u-fs-xs text-muted">主题</span>
        <div class="inline-flex [border:1px_solid_var(--border)] rounded-md overflow-hidden">
          {#each themes as t, i}
            <button
              class={theme === t.key ? (i === 0 ? segOnFirst : segOnNext) : (i === 0 ? segOffFirst : segOffNext)}
              onclick={() => {
                theme = t.key;
                setTheme(t.key);
              }}
            >
              {t.label}
            </button>
          {/each}
        </div>
      </div>
      <div class="flex items-center gap-[var(--sp-3)] mb-[var(--sp-2)]">
        <span class="flex-[0_0_3rem] u-fs-xs text-muted">字号</span>
        <div class="inline-flex [border:1px_solid_var(--border)] rounded-md overflow-hidden">
          {#each fonts as f, i}
            <button
              class={font === f.key ? (i === 0 ? segOnFirst : segOnNext) : (i === 0 ? segOffFirst : segOffNext)}
              onclick={() => {
                font = f.key;
                setFont(f.key);
              }}
            >
              {f.label}
            </button>
          {/each}
        </div>
      </div>
      <div class="flex items-center gap-[var(--sp-3)] mb-[var(--sp-2)]">
        <span class="flex-[0_0_3rem] u-fs-xs text-muted">强调色</span>
        <div class="inline-flex gap-[var(--sp-2)]" role="group" aria-label="强调色">
          {#each accents as a}
            <button
              class={accent === a.key ? swatchOn : swatchOff}
              style={`--swatch: ${a.swatch}`}
              title={a.label}
              aria-label={`强调色：${a.label}`}
              aria-pressed={accent === a.key}
              onclick={() => {
                accent = a.key;
                setAccent(a.key);
              }}
            >
              {#if accent === a.key}<span class="text-[#fff] text-[length:0.8rem] font-bold [text-shadow:0_1px_2px_rgb(0_0_0_/_0.4)]">✓</span>{/if}
            </button>
          {/each}
        </div>
      </div>
      <p class={note}>偏好保存在这台机器的浏览器里，不影响其他设备。</p>
    </section>

    <section class={block}>
      <h3 class={h3}>版本与运行状态</h3>
      <dl class={kv}>
        <dt class={dt}>版本</dt>
        <dd class={dd}><code class={codeKv}>{info.version}</code></dd>
        <dt class={dt}>运行时长</dt>
        <dd class={dd}>{uptime(info.uptime_sec)}</dd>
        <dt class={dt}>平台</dt>
        <dd class={dd}>{info.os} / {info.arch} · {info.go_version}</dd>
        <dt class={dt}>健康检查</dt>
        <dd class={dd}>
          {#if health}
            <span class={dotOk}></span> 正常 · SSE 缓冲 {health.stream_entries} 条 / {formatSize(health.stream_bytes)}
          {:else}
            <span class={dotBad}></span> 无法获取
          {/if}
        </dd>
      </dl>
    </section>

    <section class={block}>
      <h3 class={h3}>文件位置</h3>
      <dl class={kv}>
        <dt class={dt}>数据目录</dt>
        <dd class={dd}>
          <code class={codeKv}>{info.data_dir}</code>
          <button class={linkBtn} onclick={() => copy(info.data_dir, 'data')}>
            {copied === 'data' ? '已复制' : '复制'}
          </button>
        </dd>
        <dt class={dt}>数据库</dt>
        <dd class={dd}><code class={codeKv}>{info.db_path}</code> · {formatSize(info.db_bytes)}</dd>
        <dt class={dt}>配置文件</dt>
        <dd class={dd}>
          {#if info.config_path}
            <code class={codeKv}>{info.config_path}</code>
            <button class={linkBtn} onclick={() => copy(info.config_path, 'cfg')}>
              {copied === 'cfg' ? '已复制' : '复制'}
            </button>
          {:else}
            <span class="text-faint">（使用默认值，没有配置文件）</span>
          {/if}
        </dd>
        <dt class={dt}>日志</dt>
        <dd class={dd}>
          <code class={codeKv}>{info.log_file}</code>
          <button class={linkBtn} onclick={() => copy(info.log_file, 'log')}>
            {copied === 'log' ? '已复制' : '复制'}
          </button>
        </dd>
        <dt class={dt}>备份</dt>
        <dd class={dd}>{info.backups} 个（在数据目录的 <code class={codeKv}>backups/</code> 下）</dd>
      </dl>
    </section>

    <section class={block}>
      <h3 class={h3}>排障</h3>
      <p class={note}>
        导出诊断包：系统信息 + 脱敏后的配置 + 最近日志。
        <strong class="text-fg-secondary">不含对话内容与 API Key</strong>，适合发给别人帮忙排查。
      </p>
      <a class={ghost} href={api.diagnosticsURL()} download>
        <Icon name="check" size={14} /> 导出诊断包
      </a>
    </section>

    <section class={block}>
      <h3 class={h3}>退出登录</h3>
      <p class={note}>清除当前的登录状态，下次打开需要重新输入口令。</p>
      <button class={ghost} onclick={logout}>退出登录</button>
    </section>

    <section class={dangerBlock}>
      <h3 class={h3}>清空数据</h3>
      <p class={note}>
        不可逆。无论选哪一档，<strong class="text-fg-secondary">都不会删除访问口令、已保存的 API Key
        和价格设置</strong>——清掉它们等于把你锁在门外、或让配好的模型失效。
      </p>

      <div class="flex flex-col gap-[var(--sp-1)] my-[var(--sp-2)]">
        <label class={resetScope === 'sessions' ? resetOn : resetOff}>
          <input class="mt-[2px] cursor-pointer" type="radio" bind:group={resetScope} value="sessions" />
          <span class="flex flex-col gap-[1px]">
            <strong class="text-fg font-semibold">只清对话</strong>
            <em class="not-italic text-muted">会话、消息、轮次、调用记录、上下文快照</em>
          </span>
        </label>
        <label class={resetScope === 'knowledge' ? resetOn : resetOff}>
          <input class="mt-[2px] cursor-pointer" type="radio" bind:group={resetScope} value="knowledge" />
          <span class="flex flex-col gap-[1px]">
            <strong class="text-fg font-semibold">只清记忆与知识库</strong>
            <em class="not-italic text-muted">记住的事实、情节、上传的文档与向量</em>
          </span>
        </label>
        <label class={resetScope === 'all' ? resetOn : resetOff}>
          <input class="mt-[2px] cursor-pointer" type="radio" bind:group={resetScope} value="all" />
          <span class="flex flex-col gap-[1px]">
            <strong class="text-fg font-semibold">清空以上全部</strong>
            <em class="not-italic text-muted">保留口令、API Key、价格与技能/插件配置</em>
          </span>
        </label>
      </div>

      {#if resetScope}
        <div class="flex items-end gap-[var(--sp-2)] mt-[var(--sp-2)] flex-wrap">
          <label class="flex flex-col gap-[3px] u-fs-xs text-muted flex-1 min-w-[10rem]">
            <span>输入 <code>{resetScope}</code> 确认</span>
            <input class="py-[6px] px-[var(--sp-2)] [border:1px_solid_var(--border-strong)] rounded-sm [background:var(--bg)] text-fg font-mono [outline:none] focus:border-danger" bind:value={resetConfirm} placeholder={resetScope} />
          </label>
          <button
            class={dangerBtn}
            onclick={doReset}
            disabled={resetting || resetConfirm !== resetScope}
          >
            {resetting ? '清空中…' : '确认清空'}
          </button>
        </div>
      {/if}

      {#if resetResult}
        <p class="flex items-center flex-wrap gap-[6px] [margin:var(--sp-2)_0_0] u-fs-xs text-success">
          <Icon name="check" size={14} />
          已清空。删除行数：
          {#each Object.entries(resetResult.summary) as [table, n]}
            {#if n > 0}<code class="font-mono bg-surface-2 py-[1px] px-[5px] rounded-[3px]">{table} {n}</code>{/if}
          {/each}
        </p>
      {/if}
    </section>
  {/if}
</div>

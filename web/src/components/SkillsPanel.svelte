<script>
  // 技能管理：只读列表 + 启停开关 + 重新扫描。
  //
  // 不做在线编辑：技能是文件，用户有文件管理器。这里只解决「不知道
  // 有什么」和「禁用要改配置」两件事。
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';
  import { humanize } from '../lib/errors.js';

  let data = $state(null); // { skills, root }
  let loading = $state(true);
  let error = $state('');
  let busy = $state(''); // 正在切换的技能名
  let copied = $state(false);

  async function load() {
    loading = true;
    error = '';
    try {
      data = await api.skills();
    } catch (err) {
      error = humanize(err);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });

  async function toggle(sk) {
    if (busy) return;
    busy = sk.name;
    error = '';
    try {
      await api.setSkillEnabled(sk.name, !sk.enabled);
      // 局部更新即可，避免整表重载导致列表闪烁
      data = {
        ...data,
        skills: data.skills.map((s) => (s.name === sk.name ? { ...s, enabled: !s.enabled } : s)),
      };
    } catch (err) {
      error = humanize(err);
    } finally {
      busy = '';
    }
  }

  async function reload() {
    loading = true;
    error = '';
    try {
      data = await api.reloadSkills();
    } catch (err) {
      error = humanize(err);
    } finally {
      loading = false;
    }
  }

  async function copyRoot() {
    if (!data?.root) return;
    try {
      await navigator.clipboard.writeText(data.root);
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch {
      // 剪贴板不可用（非 https 或权限拒绝）时退回选中文本
      error = '复制失败，请手动选中路径复制：' + data.root;
    }
  }
</script>

<div class="flex flex-col gap-[var(--sp-3)]">
  <div class="flex items-start justify-between gap-[var(--sp-3)]">
    <p class="m-0 u-fs-xs text-muted leading-[1.6]">
      技能是 Markdown 文件。往目录里放一个 <code class="font-mono bg-surface-2 py-px px-[4px] rounded-[3px]">SKILL.md</code> 就多一项能力，
      不需要重新编译。
    </p>
    <button class="inline-flex items-center gap-[5px] py-[6px] px-[12px] rounded-md cursor-pointer u-fs-sm [border:1px_solid_var(--border)] bg-surface text-fg whitespace-nowrap enabled:hover:bg-surface-hover disabled:opacity-50 disabled:cursor-default" onclick={reload} disabled={loading}>
      <Icon name="search" size={14} /> 重新扫描
    </button>
  </div>

  {#if error}
    <div class="flex items-center gap-[6px] py-[var(--sp-2)] px-[var(--sp-3)] bg-danger-soft text-danger rounded-sm u-fs-sm"><Icon name="alert" size={14} /> {error}</div>
  {/if}

  {#if loading && !data}
    <p class="flex items-center gap-[6px] m-0 u-fs-sm text-muted"><Icon name="clock" size={14} /> 加载中…</p>
  {:else if data && data.skills.length === 0}
    <div class="p-[var(--sp-5)] [border:1px_dashed_var(--border-strong)] rounded-md text-center">
      <p class="m-0 mb-[var(--sp-2)] u-fs-sm text-fg-secondary">还没有技能。</p>
      <p class="m-0 mb-[var(--sp-2)] u-fs-sm text-fg-secondary leading-[1.6]">把技能目录放进下面这个位置，再点「重新扫描」：</p>
      <div class="flex items-center justify-center gap-[var(--sp-2)] mt-[var(--sp-2)]">
        <code class="font-mono u-fs-xs bg-surface-2 py-[4px] px-[8px] rounded-sm [word-break:break-all]">{data.root}</code>
        <button class="inline-flex items-center gap-[5px] py-[3px] px-[9px] rounded-md cursor-pointer u-fs-xs [border:1px_solid_var(--border)] bg-surface text-fg whitespace-nowrap enabled:hover:bg-surface-hover" onclick={copyRoot}>{copied ? '已复制' : '复制路径'}</button>
      </div>
    </div>
  {:else if data}
    <div class="flex flex-col gap-[var(--sp-2)]">
      {#each data.skills as sk}
        <section class="py-[var(--sp-3)] px-[var(--sp-4)] [border:1px_solid_var(--border)] rounded-md bg-surface {sk.enabled ? '' : 'opacity-[0.62]'}">
          <header class="flex items-center justify-between gap-[var(--sp-3)]">
            <div class="flex items-baseline gap-[var(--sp-2)] min-w-0">
              <span class="font-semibold u-fs-sm font-mono">{sk.name}</span>
              {#if sk.version}<span class="u-fs-xs text-faint">v{sk.version}</span>{/if}
            </div>
            <label class="inline-flex items-center gap-[6px] u-fs-xs text-fg-secondary cursor-pointer whitespace-nowrap" title={sk.enabled ? '点击禁用' : '点击启用'}>
              <input
                type="checkbox"
                checked={sk.enabled}
                disabled={busy === sk.name}
                class="cursor-pointer"
                onchange={() => toggle(sk)}
              />
              <span>{sk.enabled ? '已启用' : '已禁用'}</span>
            </label>
          </header>

          <p class="mt-[6px] mb-0 u-fs-sm text-fg-secondary leading-[1.6]">{sk.description}</p>

          <div class="flex flex-wrap gap-[6px] mt-[var(--sp-2)]">
            {#if sk.triggers?.length}
              <span class="u-fs-xs text-muted bg-surface-2 py-px px-[7px] rounded-full">触发词：{sk.triggers.join('、')}</span>
            {/if}
            {#if sk.requires?.length}
              <span class="u-fs-xs text-warning bg-surface-2 py-px px-[7px] rounded-full">需要工具：{sk.requires.join('、')}</span>
            {/if}
            {#if sk.references > 0}
              <span class="u-fs-xs text-muted bg-surface-2 py-px px-[7px] rounded-full">参考文件 {sk.references} 个</span>
            {/if}
          </div>

          <p class="mt-[6px] mb-0 text-[0.7rem] font-mono text-faint overflow-hidden text-ellipsis whitespace-nowrap" title={sk.dir}>{sk.dir}</p>
        </section>
      {/each}
    </div>

    <p class="m-0 u-fs-xs text-faint">
      技能目录：<code class="font-mono">{data.root}</code>
      <button class="[background:none] [border:none] text-accent cursor-pointer u-fs-xs px-[4px] py-0 hover:underline" onclick={copyRoot}>{copied ? '已复制' : '复制'}</button>
    </p>
  {/if}
</div>


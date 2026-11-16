<script>
  // 数据出口：导出会话与本地备份。
  //
  // 恢复不放这里：它要停库换文件，界面里做会在运行中的进程里
  // 覆盖数据。恢复用 `baiyi restore <路径>`，页面只负责告诉用户
  // 备份在哪。
  import Icon from './Icon.svelte';
  import { api } from '../lib/api.js';
  import { humanize } from '../lib/errors.js';
  import { formatSize } from '../lib/format.js';

  let backups = $state(null);
  let loading = $state(true);
  let error = $state('');
  let busy = $state(false);
  let latest = $state(null);

  async function load() {
    loading = true;
    error = '';
    try {
      const res = await api.backups();
      backups = res.backups || [];
    } catch (err) {
      error = humanize(err);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });

  async function createBackup() {
    busy = true;
    error = '';
    latest = null;
    try {
      latest = await api.createBackup();
      await load();
    } catch (err) {
      error = humanize(err);
    } finally {
      busy = false;
    }
  }
</script>

<div class="flex flex-col gap-[var(--sp-4)]">
  <section class="u-box py-[var(--sp-3)] px-[var(--sp-4)]">
    <h3 class="[margin:0_0_var(--sp-2)] u-fs-sm font-semibold">导出会话</h3>
    <p class="[margin:0_0_var(--sp-2)] u-fs-xs text-muted leading-[1.7]">
      导出的是对话内容本身：消息、分支、时间。密钥与配置不在其中。
    </p>
    <div class="flex gap-[var(--sp-2)] my-[var(--sp-2)]">
      <a
        class="inline-flex items-center gap-[5px] py-[6px] px-[12px] rounded-md cursor-pointer u-fs-sm whitespace-nowrap no-underline [border:1px_solid_var(--border)] bg-surface text-fg hover:bg-surface-hover"
        href={api.exportAllURL()}
        download
      >
        <Icon name="check" size={14} /> 导出全部会话（JSONL）
      </a>
    </div>
    <p class="[margin:var(--sp-2)_0_0] u-fs-xs text-faint">
      单个会话在左侧列表里：把鼠标移到会话上，点「导出」图标即可下载 Markdown。
    </p>
  </section>

  <section class="u-box py-[var(--sp-3)] px-[var(--sp-4)]">
    <h3 class="[margin:0_0_var(--sp-2)] u-fs-sm font-semibold">备份数据库</h3>
    <p class="[margin:0_0_var(--sp-2)] u-fs-xs text-muted leading-[1.7]">
      备份是数据库的完整快照，包含会话、记忆、知识库与加密后的密钥。
      生成的文件在数据目录的
      <code class="font-mono bg-surface-2 py-px px-[4px] rounded-[3px]">backups/</code>
      下。
    </p>

    <div class="flex gap-[var(--sp-2)] my-[var(--sp-2)]">
      <button
        class="inline-flex items-center gap-[5px] py-[6px] px-[12px] rounded-md cursor-pointer u-fs-sm whitespace-nowrap [border:1px_solid_var(--accent)] bg-accent text-accent-fg enabled:hover:bg-accent-hover disabled:opacity-50 disabled:cursor-default"
        onclick={createBackup}
        disabled={busy}
      >
        {busy ? '备份中…' : '立即备份'}
      </button>
    </div>

    {#if latest}
      <p class="flex items-center flex-wrap gap-[6px] [margin:var(--sp-2)_0_0] u-fs-xs text-success">
        <Icon name="check" size={14} /> 已生成：<code class="font-mono break-all">{latest.path}</code>
        （{formatSize(latest.size)}）
      </p>
    {/if}

    {#if error}
      <div class="flex items-center gap-[6px] mt-[var(--sp-2)] py-[var(--sp-2)] px-[var(--sp-3)] bg-danger-soft text-danger rounded-sm u-fs-sm">
        <Icon name="alert" size={14} /> {error}
      </div>
    {/if}

    {#if loading && !backups}
      <p class="flex items-center gap-[6px] [margin:var(--sp-2)_0_0] u-fs-xs text-muted">
        <Icon name="clock" size={14} /> 加载中…
      </p>
    {:else if backups && backups.length}
      <ul class="list-none [margin:var(--sp-2)_0_0] p-0 flex flex-col gap-[4px]">
        {#each backups as b}
          <li class="flex items-baseline gap-[var(--sp-2)] u-fs-xs py-[4px] [border-top:1px_solid_var(--border)]">
            <span class="text-fg-secondary whitespace-nowrap">{b.mod_time}</span>
            <span class="text-muted whitespace-nowrap">{formatSize(b.size)}</span>
            <code
              class="font-mono text-[0.7rem] text-faint overflow-hidden text-ellipsis whitespace-nowrap [direction:rtl] text-left"
              title={b.path}>{b.path}</code>
          </li>
        {/each}
      </ul>
    {:else if backups}
      <p class="[margin:var(--sp-2)_0_0] u-fs-xs text-muted">还没有备份。</p>
    {/if}

    <details class="mt-[var(--sp-3)] pt-[var(--sp-2)] [border-top:1px_solid_var(--border)] u-fs-xs">
      <summary class="cursor-pointer text-fg-secondary">怎么恢复？</summary>
      <p class="[margin:0_0_var(--sp-2)] u-fs-xs text-muted leading-[1.7]">
        恢复需要先停止白泽，再在命令行里执行：
      </p>
      <pre class="[margin:var(--sp-2)_0] p-[var(--sp-2)] bg-bg rounded-sm overflow-x-auto"><code
          class="font-mono [background:none] p-0">baiyi restore &lt;备份文件路径&gt;</code></pre>
      <p class="[margin:0_0_var(--sp-2)] u-fs-xs text-muted leading-[1.7]">
        原数据库会被改名保留（<code class="font-mono bg-surface-2 py-px px-[4px] rounded-[3px]">baiyi.db.before-restore-*</code>），
        恢复错了还能换回来。恢复完成后启动白泽即可。
      </p>
      <p class="[margin:0_0_var(--sp-2)] u-fs-xs text-warning leading-[1.7]">
        加密密钥与当前 Windows 用户绑定。把备份拿到另一台机器或
        另一个用户下恢复，会话数据能读，但已保存的 API Key 解不开，
        需要重新填写。
      </p>
    </details>
  </section>
</div>

// 能力概览。纯展示 + 深链，不新建后端接口。
//
// 解决的是「能力都存在，但用户不知道」：把散在 10 个 tab 后面的
// 东西摆在一屏里，每条配一句人话说明与一个直达入口。
<script>
  import Icon from './Icon.svelte';
  import { navGroups, itemsOfGroup } from '../lib/nav.js';
  import { loadOverview } from '../lib/status.js';

  let { onNavigate = null } = $props();

  let live = $state(null);

  $effect(() => {
    loadOverview().then((v) => (live = v));
  });
</script>

<div class="flex flex-col gap-[var(--sp-5)] max-w-[52rem] mx-auto">
  <header class="text-center pt-[var(--sp-4)] pb-[var(--sp-2)]">
    <div class="font-brand text-[1.5rem] tracking-[0.35em] indent-[0.35em] text-brand mb-[var(--sp-2)]">白泽</div>
    <p class="[margin:0_auto] max-w-[34rem] u-fs-sm text-muted leading-[1.75]">
      本地运行的个人助理。模型由你配置，数据只留在这台机器。
      下面这些是它已经具备的能力——点标题可以直接去配置或查看。
    </p>
  </header>

  {#if live}
    <div class="grid grid-cols-5 gap-[var(--sp-2)] max-[700px]:grid-cols-3">
      <div class="u-box [box-shadow:var(--shadow-sm)] flex flex-col items-center gap-[2px] py-[var(--sp-3)] px-[var(--sp-2)]">
        <strong class="text-[1.25rem] font-semibold text-accent [font-variant-numeric:tabular-nums]">{live.models}</strong>
        <span class="u-fs-xs text-muted">可用模型</span>
      </div>
      <div class="u-box [box-shadow:var(--shadow-sm)] flex flex-col items-center gap-[2px] py-[var(--sp-3)] px-[var(--sp-2)]">
        <strong class="text-[1.25rem] font-semibold text-accent [font-variant-numeric:tabular-nums]">{live.skills}</strong>
        <span class="u-fs-xs text-muted">启用技能</span>
      </div>
      <div class="u-box [box-shadow:var(--shadow-sm)] flex flex-col items-center gap-[2px] py-[var(--sp-3)] px-[var(--sp-2)]">
        <strong class="text-[1.25rem] font-semibold text-accent [font-variant-numeric:tabular-nums]">{live.plugins}</strong>
        <span class="u-fs-xs text-muted">已连插件</span>
      </div>
      <div class="u-box [box-shadow:var(--shadow-sm)] flex flex-col items-center gap-[2px] py-[var(--sp-3)] px-[var(--sp-2)]">
        <strong class="text-[1.25rem] font-semibold text-accent [font-variant-numeric:tabular-nums]">{live.tools}</strong>
        <span class="u-fs-xs text-muted">外部工具</span>
      </div>
      <div class="u-box [box-shadow:var(--shadow-sm)] flex flex-col items-center gap-[2px] py-[var(--sp-3)] px-[var(--sp-2)]">
        <strong class="text-[1.25rem] font-semibold text-accent [font-variant-numeric:tabular-nums]">{live.facts}</strong>
        <span class="u-fs-xs text-muted">记住的事实</span>
      </div>
    </div>
  {/if}

  {#each navGroups as group}
    <section>
      <h3 class="[margin:0_0_var(--sp-2)] u-fs-xs text-faint tracking-[0.08em] font-semibold">{group.label}</h3>
      <div class="grid grid-cols-2 gap-[var(--sp-2)] max-[700px]:grid-cols-[1fr]">
        {#each itemsOfGroup(group.key) as it}
          <button
            class="item flex items-start gap-[var(--sp-2)] text-left p-[var(--sp-3)] u-box cursor-pointer text-muted hover:bg-surface-hover hover:border-accent"
            onclick={() => onNavigate?.(it.key)}
          >
            <span class="flex-none grid place-items-center w-[28px] h-[28px] rounded-sm bg-accent-soft text-accent">
              <Icon name={it.icon} size={17} />
            </span>
            <span class="flex-1 min-w-0 flex flex-col gap-[2px]">
              <span class="u-fs-sm font-semibold text-fg">{it.label}</span>
              <span class="u-fs-xs text-muted leading-[1.5]">{it.desc}</span>
            </span>
            <Icon name="chevronRight" size={14} />
          </button>
        {/each}
      </div>
    </section>
  {/each}

  <section>
    <h3 class="[margin:0_0_var(--sp-2)] u-fs-xs text-faint tracking-[0.08em] font-semibold">对话里会遇到的</h3>
    <div class="grid grid-cols-2 gap-[var(--sp-2)] max-[700px]:grid-cols-[1fr]">
      <div class="flex items-start gap-[var(--sp-2)] p-[var(--sp-3)] [background:var(--bg-subtle)] rounded-md text-accent">
        <Icon name="route" size={15} />
        <div>
          <strong class="block u-fs-sm text-fg">分支</strong>
          <p class="[margin:2px_0_0] u-fs-xs text-muted leading-[1.6]">编辑或重新生成会留下另一版回答，可以来回切换，不会覆盖原来的。</p>
        </div>
      </div>
      <div class="flex items-start gap-[var(--sp-2)] p-[var(--sp-3)] [background:var(--bg-subtle)] rounded-md text-accent">
        <Icon name="shield" size={15} />
        <div>
          <strong class="block u-fs-sm text-fg">审批</strong>
          <p class="[margin:2px_0_0] u-fs-xs text-muted leading-[1.6]">写入或危险操作会先弹窗问你；接触外部内容后永久授权自动失效。</p>
        </div>
      </div>
      <div class="flex items-start gap-[var(--sp-2)] p-[var(--sp-3)] [background:var(--bg-subtle)] rounded-md text-accent">
        <Icon name="link" size={15} />
        <div>
          <strong class="block u-fs-sm text-fg">引用</strong>
          <p class="[margin:2px_0_0] u-fs-xs text-muted leading-[1.6]">回答里的数字标记可点开，能跳回知识库原文的确切片段。</p>
        </div>
      </div>
      <div class="flex items-start gap-[var(--sp-2)] p-[var(--sp-3)] [background:var(--bg-subtle)] rounded-md text-accent">
        <Icon name="refresh" size={15} />
        <div>
          <strong class="block u-fs-sm text-fg">断线续跑</strong>
          <p class="[margin:2px_0_0] u-fs-xs text-muted leading-[1.6]">浏览器连接断了会自动重连；进程重启后中断的轮次也能接着跑。</p>
        </div>
      </div>
    </div>
  </section>
</div>

<style>
  /* 过渡的时长和缓动写在一条里，原子类拆开后计算值和原来对不齐。 */
  .item {
    transition: border-color 0.15s ease, background 0.15s ease;
  }
</style>

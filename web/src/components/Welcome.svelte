<script>
  // 首屏。这里承担「让用户知道白泽能做什么」的责任：
  // 能力卡片按用户能理解的话写，状态条显示当前实际装备了什么。
  // 以前只有一句「我能查时间」和四张写着内部工具名的卡片，
  // 技能/插件/上下文/分支/审批/成本在首屏零次提及。
  import Icon from './Icon.svelte';
  import { sendMessage } from '../lib/store.svelte.js';
  import { api } from '../lib/api.js';
  import { loadOverview } from '../lib/status.js';
  import SetupWizard from './SetupWizard.svelte';

  let { onOpenSettings = null, onOpenCapabilities = null } = $props();

  let models = $state(null);
  let showWizard = $state(false);
  let status = $state(null);

  function loadModels() {
    api
      .configuredModels()
      .then((res) => {
        models = res.models || [];
      })
      .catch(() => {
        // 查不到就按「已配置」处理，让主流程自己报错
        models = null;
      });
  }

  // 状态条与能力一览共用一份聚合结果，避免重复请求与口径分叉
  async function loadStatus() {
    status = await loadOverview();
  }

  $effect(() => {
    loadModels();
    loadStatus();
  });

  /** 能力卡：按用户能理解的话写，不写内部工具名。 */
  const capabilities = [
    {
      icon: 'book',
      title: '问我的文档',
      desc: '导入的知识库会带上可点击的出处',
      prompt: '帮我从知识库里找一下相关内容',
    },
    {
      icon: 'brain',
      title: '记得住我说过的话',
      desc: '偏好与事实跨会话保留，也能改、能撤销',
      prompt: '我们之前聊过什么？',
    },
    {
      icon: 'sparkle',
      title: '按需加载技能',
      desc: '放一个 SKILL.md 就多一项专长，不用重启',
      open: 'skills',
    },
    {
      icon: 'tool',
      title: '接外部工具',
      desc: '通过插件调用文件系统、数据库或你自己的服务',
      open: 'mcp',
    },
    {
      icon: 'layers',
      title: '看得见上下文',
      desc: '每一轮模型收到什么、为什么裁剪，都能回看',
      open: 'context',
    },
    {
      icon: 'chart',
      title: '用量算得清',
      desc: '按模型拆分 token 与花费，按当时价格计算',
      open: 'cost',
    },
  ];

  async function activate(card) {
    if (card.open) {
      onOpenSettings?.(card.open);
      return;
    }
    await sendMessage(card.prompt);
  }
</script>

{#if showWizard}
  <SetupWizard
    onDone={() => {
      showWizard = false;
      loadModels();
      loadStatus();
    }}
  />
{:else}
  <div class="flex-1 flex flex-col items-center justify-center p-[var(--sp-5)] gap-[var(--sp-4)] min-h-0 max-[640px]:gap-[var(--sp-3)] max-[640px]:p-[var(--sp-4)]">
    {#if models && models.length === 0}
      <div class="text-center">
        <div class="grid place-items-center mx-auto mb-[var(--sp-2)]" aria-hidden="true">
          <svg viewBox="0 0 64 64" width="64" height="64" fill="none">
            <path d="M32 4 58 18v28L32 60 6 46V18L32 4z" stroke="var(--brand)" stroke-width="1.5" opacity=".45" />
            <path d="M32 14 48 23v18l-16 9-16-9V23l16-9z" stroke="var(--accent)" stroke-width="1.8" />
            <path d="M32 14v36M16 23l32 18M48 23 16 41" stroke="var(--accent)" stroke-width="1" opacity=".35" />
            <circle cx="32" cy="32" r="4" fill="var(--accent)" />
          </svg>
        </div>
        <div class="font-brand text-[1.5rem] tracking-[0.35em] indent-[0.35em] text-brand mb-[var(--sp-3)]">白泽</div>
        <h1 class="m-0 mb-[var(--sp-2)] text-[1.5rem] font-semibold tracking-[-0.01em] text-fg">先接一个模型服务</h1>
        <p class="m-0 text-muted u-fs-sm leading-[1.75] max-w-[34rem]">
          白泽自己不产生回答——它调用你配置的模型服务。<br />
          大约一分钟，之后就能在这里问任何事。
        </p>
        <button class="mt-[var(--sp-4)] py-[9px] px-[20px] bg-accent text-accent-fg [border:none] rounded-md u-fs-sm cursor-pointer [box-shadow:var(--shadow-sm)] hover:bg-accent-hover" onclick={() => (showWizard = true)}>开始配置</button>
        <p class="mt-[var(--sp-3)] mb-0 u-fs-xs text-faint">
          也可以直接改配置文件：<code class="font-mono">%APPDATA%\baiyi\config.toml</code>，
          或<button class="[background:none] [border:none] p-0 [font:inherit] text-accent no-underline cursor-pointer hover:underline" onclick={() => onOpenSettings?.('providers')}>打开模型服务设置</button>
        </p>
      </div>
    {:else}
      <div class="text-center">
        <div class="grid place-items-center mx-auto mb-[var(--sp-2)]" aria-hidden="true">
          <svg viewBox="0 0 64 64" width="64" height="64" fill="none">
            <path d="M32 4 58 18v28L32 60 6 46V18L32 4z" stroke="var(--brand)" stroke-width="1.5" opacity=".45" />
            <path d="M32 14 48 23v18l-16 9-16-9V23l16-9z" stroke="var(--accent)" stroke-width="1.8" />
            <path d="M32 14v36M16 23l32 18M48 23 16 41" stroke="var(--accent)" stroke-width="1" opacity=".35" />
            <circle cx="32" cy="32" r="4" fill="var(--accent)" />
          </svg>
        </div>
        <div class="font-brand text-[1.5rem] tracking-[0.35em] indent-[0.35em] text-brand mb-[var(--sp-3)]">白泽</div>
        <h1 class="m-0 mb-[var(--sp-2)] text-[1.5rem] font-semibold tracking-[-0.01em] text-fg">今天想聊点什么？</h1>
        <p class="m-0 text-muted u-fs-sm leading-[1.75] max-w-[34rem]">本地运行的个人助理：能查资料、记住偏好、调用工具，数据只留在这台机器。</p>
      </div>

      <div class="self-start w-full max-w-[44rem] mt-[var(--sp-2)] mx-auto mb-0 u-fs-xs text-faint tracking-[0.08em]">能做什么</div>
      <div class="grid grid-cols-[1fr] min-[641px]:grid-cols-2 min-[901px]:grid-cols-3 gap-[var(--sp-2)] w-full max-w-[44rem]">
        {#each capabilities as card}
          <button
            class="flex items-start gap-[var(--sp-2)] text-left p-[var(--sp-3)] bg-surface [border:1px_solid_var(--border)] rounded-md [box-shadow:var(--shadow-sm)] cursor-pointer text-muted [transition:border-color_0.15s_ease,background_0.15s_ease,transform_0.15s_ease] hover:bg-surface-hover hover:border-accent hover:[transform:translateY(-2px)]"
            onclick={() => activate(card)}
          >
            <span class="flex-none grid place-items-center w-[30px] h-[30px] rounded-sm bg-accent-soft text-accent"><Icon name={card.icon} size={18} /></span>
            <span class="flex-1 min-w-0 flex flex-col gap-[2px]">
              <span class="u-fs-sm font-semibold text-fg">{card.title}</span>
              <span class="u-fs-xs text-muted leading-[1.5]">{card.desc}</span>
            </span>
            <Icon name="chevronRight" size={14} />
          </button>
        {/each}
      </div>

      {#if status}
        <div class="flex items-center flex-wrap justify-center gap-[var(--sp-2)] w-full max-w-[44rem] py-[8px] px-[var(--sp-3)] bg-surface [border:1px_solid_var(--border)] rounded-full u-fs-xs text-muted" aria-label="当前状态">
          <span class="inline-flex items-center gap-[4px] whitespace-nowrap">
            <span class="w-[7px] h-[7px] [border-radius:50%] {status.online ? 'bg-success' : 'bg-faint'}"></span>
            {status.online ? '服务正常' : '状态未知'}
          </span>
          <span class="w-px h-[12px] bg-border max-[640px]:hidden"></span>
          <span class="inline-flex items-center gap-[4px] whitespace-nowrap">已启用 <strong class="text-fg-secondary font-semibold">{status.skills}</strong> 项技能</span>
          <span class="w-px h-[12px] bg-border max-[640px]:hidden"></span>
          <span class="inline-flex items-center gap-[4px] whitespace-nowrap">已连接 <strong class="text-fg-secondary font-semibold">{status.plugins}</strong> 个插件</span>
          <span class="w-px h-[12px] bg-border max-[640px]:hidden"></span>
          <span class="inline-flex items-center gap-[4px] whitespace-nowrap">记住 <strong class="text-fg-secondary font-semibold">{status.facts}</strong> 条事实</span>
          <span class="w-px h-[12px] bg-border max-[640px]:hidden"></span>
          <span class="inline-flex items-center gap-[4px] whitespace-nowrap">今日 <strong class="text-fg-secondary font-semibold">{status.calls}</strong> 次调用</span>
        </div>
      {/if}

      <button class="inline-flex items-center gap-[5px] [background:none] [border:none] text-accent u-fs-xs cursor-pointer hover:underline" onclick={() => onOpenCapabilities?.()}>
        <Icon name="help" size={14} /> 看完整的能力一览
      </button>
    {/if}
  </div>
{/if}


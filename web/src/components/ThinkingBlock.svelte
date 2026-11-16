<script>
  import Icon from './Icon.svelte';

  let { text } = $props();
  let open = $state(false);
  let seconds = $derived(Math.max(1, Math.round((text || '').length / 300)));
</script>

<div class="mb-[var(--sp-3)]">
  <button
    class="inline-flex items-center gap-[6px] bg-surface-2 [border:1px_solid_var(--border)] rounded-full py-[5px] px-[12px] cursor-pointer text-fg-secondary u-fs-sm [transition:background_0.15s_ease] hover:bg-surface-hover"
    onclick={() => (open = !open)}
    aria-expanded={open}
  >
    <Icon name={open ? 'chevronDown' : 'chevronRight'} size={14} />
    <span>已深度思考</span>
    <span class="text-muted">（约 {seconds} 秒）</span>
  </button>
  {#if open}
    <div
      class="mt-[var(--sp-2)] py-[var(--sp-3)] px-[var(--sp-4)] [border-left:2px_solid_var(--border-strong)] text-fg-secondary u-fs-sm leading-[1.7] whitespace-pre-wrap [word-break:break-word]"
    >{text}</div>
  {/if}
</div>

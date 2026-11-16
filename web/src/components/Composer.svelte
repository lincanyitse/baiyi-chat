<script>
  import Icon from './Icon.svelte';
  import {
    chatState,
    sendMessage,
    stopGeneration,
    addAttachment,
    removeAttachment,
  } from '../lib/store.svelte.js';
  import { api } from '../lib/api.js';

  let text = $state('');
  let box = $state(null);
  let fileInput = $state(null);
  let uploading = $state(false);
  let attachError = $state('');

  // 输入框随内容长高，到上限后内部滚动
  const MAX_HEIGHT = 200;
  $effect(() => {
    if (!box) return;
    box.style.height = 'auto';
    box.style.height = Math.min(box.scrollHeight, MAX_HEIGHT) + 'px';
  });

  async function submit() {
    const value = text.trim();
    const hasAttachments = chatState.pendingAttachments.length > 0;
    if ((!value && !hasAttachments) || chatState.sending) return;
    text = '';
    await sendMessage(value);
    box?.focus();
  }

  function onKeydown(e) {
    // Enter 发送、Shift+Enter 换行；输入法组字过程中的 Enter 必须放行，
    // 否则中文候选词一确认就把半句话发出去了。
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      submit();
    }
  }

  async function pickFiles(files) {
    if (!files?.length) return;
    uploading = true;
    attachError = '';
    try {
      for (const f of files) {
        await addAttachment(f);
      }
    } catch (err) {
      attachError = err.message;
    } finally {
      uploading = false;
      if (fileInput) fileInput.value = '';
    }
  }

  function onFilePick(e) {
    pickFiles(e.currentTarget.files);
  }

  function onDrop(e) {
    e.preventDefault();
    pickFiles(e.dataTransfer.files);
  }

  const canSend = $derived(
    (!!text.trim() || chatState.pendingAttachments.length > 0) && !chatState.sending
  );
</script>

<div
  class="pt-0 px-[var(--sp-5)] pb-[var(--sp-4)] flex-none max-[640px]:px-[var(--sp-3)] max-[640px]:pb-[var(--sp-3)]"
>
  {#if chatState.pendingAttachments.length > 0 || attachError}
    <div class="max-w-[var(--reading-width)] mx-auto mb-[var(--sp-2)] flex gap-[var(--sp-2)] flex-wrap items-center">
      {#each chatState.pendingAttachments as a (a.id)}
        <div class="thumb relative flex items-center gap-[5px] py-[4px] px-[8px] bg-surface-2 [border:1px_solid_var(--border)] rounded-md u-fs-xs text-fg-secondary" class:image={a.kind === 'image'}>
          {#if a.kind === 'image'}
            <img class="w-[28px] h-[28px] object-cover rounded-sm" src={api.attachmentURL(a.id)} alt={a.name} />
          {:else}
            <Icon name="tool" size={16} />
          {/if}
          <span class="max-w-[10rem] overflow-hidden text-ellipsis whitespace-nowrap">{a.name}</span>
          <button
            class="[background:none] [border:none] p-[2px] cursor-pointer text-muted grid place-items-center rounded-full hover:bg-surface-hover hover:text-danger"
            onclick={() => removeAttachment(a.id)}
            aria-label="移除附件"
          >
            <Icon name="close" size={11} />
          </button>
        </div>
      {/each}
      {#if attachError}<span class="text-danger u-fs-xs">{attachError}</span>{/if}
    </div>
  {/if}

  <div
    class="composer max-w-[var(--reading-width)] mx-auto flex items-end gap-[var(--sp-2)] p-[var(--sp-2)] bg-surface [border:1px_solid_var(--border-strong)] rounded-lg [box-shadow:var(--shadow-sm)] [transition:border-color_0.15s_ease,box-shadow_0.15s_ease] focus-within:border-accent focus-within:[box-shadow:0_0_0_3px_var(--accent-soft)]"
    class:sending={chatState.sending}
    role="group"
    aria-label="消息输入"
    ondragover={(e) => e.preventDefault()}
    ondrop={onDrop}
  >
    <input
      type="file"
      bind:this={fileInput}
      onchange={onFilePick}
      multiple
      accept="image/png,image/jpeg,image/gif,image/webp,.md,.txt,.html,.pdf"
      hidden
    />
    <button
      class="flex-none w-[34px] h-[34px] [border:none] [background:none] rounded-full text-muted cursor-pointer grid place-items-center enabled:hover:bg-surface-2 enabled:hover:text-fg disabled:opacity-40 disabled:cursor-default"
      onclick={() => fileInput?.click()}
      disabled={chatState.sending || uploading}
      title="添加附件（图片或文档）"
      aria-label="添加附件"
    >
      <Icon name={uploading ? 'clock' : 'plus'} size={16} />
    </button>

    <textarea
      class="flex-1 resize-none [border:none] [background:none] [outline:none] py-[8px] px-0 min-h-[24px] max-h-[200px] leading-[1.6] overflow-y-auto placeholder:text-muted"
      bind:this={box}
      bind:value={text}
      onkeydown={onKeydown}
      placeholder="给白泽发消息"
      rows="1"
      disabled={chatState.sending}
      aria-label="输入消息"
    ></textarea>

    {#if chatState.sending}
      <button
        class="flex-none w-[34px] h-[34px] rounded-full [border:none] grid place-items-center cursor-pointer [transition:background_0.15s_ease,opacity_0.15s_ease] bg-fg text-bg hover:opacity-85"
        onclick={stopGeneration}
        title="停止生成"
        aria-label="停止生成"
      >
        <Icon name="stop" size={15} stroke={2.2} />
      </button>
    {:else}
      <button
        class="flex-none w-[34px] h-[34px] rounded-full [border:none] grid place-items-center cursor-pointer [transition:background_0.15s_ease,opacity_0.15s_ease] bg-accent text-accent-fg enabled:hover:bg-accent-hover disabled:bg-surface-2 disabled:text-faint disabled:cursor-default"
        onclick={submit}
        disabled={!canSend}
        title="发送"
        aria-label="发送"
      >
        <Icon name="send" size={16} stroke={2.2} />
      </button>
    {/if}
  </div>

  <p class="max-w-[var(--reading-width)] mt-[var(--sp-2)] mx-auto mb-0 text-center text-faint u-fs-xs max-[640px]:hidden">
    Enter 发送 · Shift + Enter 换行 · 可拖入图片或文档
  </p>
</div>

<style>
  /* 图片缩略图的内边距要压过普通附件的 py/px。 */
  .thumb.image {
    padding: 3px;
    padding-right: 8px;
  }
</style>

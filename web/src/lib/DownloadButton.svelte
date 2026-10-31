<script lang="ts">
  // Queues files or a folder for download; the outcome shows as a toast.
  import type { FileRequest } from './api';
  import { queueFiles } from './queue';
  import Icon from './Icon.svelte';

  let {
    username,
    files,
    folder = '',
    label = 'Download',
    caption = '',
    compact = false,
    primary = true,
  }: {
    username: string;
    files: FileRequest[];
    folder?: string;
    label?: string;
    caption?: string;
    compact?: boolean;
    primary?: boolean;
  } = $props();

  let busy = $state(false);
  let done = $state(false);

  async function go() {
    if (busy) return;
    busy = true;
    done = (await queueFiles(username, files, folder)) > 0 || done;
    busy = false;
  }
</script>

{#if compact}
  <button class="icon-btn small" class:ok={done} disabled={busy} onclick={go} title={done ? 'Queued' : label} aria-label={label}>
    <span class="ic" class:spin={busy}><Icon name={busy ? 'retry' : done ? 'check' : 'download'} size={17} /></span>
  </button>
{:else}
  <button class="main" class:primary disabled={busy} onclick={go}>
    <span class="ic" class:spin={busy}><Icon name={busy ? 'retry' : done ? 'check' : 'download'} size={17} /></span>
    <span class="ellipsis">{busy ? 'Queuing…' : done ? 'Queued' : label}</span>
    {#if caption && !busy && !done}<span class="caption">{caption}</span>{/if}
  </button>
{/if}

<style>
  .main {
    min-width: 0;
  }
  .ic {
    display: grid;
    place-items: center;
  }
  .caption {
    font-weight: 500;
    opacity: 0.78;
    font-variant-numeric: tabular-nums;
  }
  .caption::before {
    content: '·';
    margin-right: 6px;
  }
  .ok {
    color: var(--good);
  }
</style>

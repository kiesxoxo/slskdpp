<script lang="ts">
  // Queues files or a folder for download and says how it went.
  import { api, errorText, type FileRequest } from './api';
  import { href } from './router.svelte';

  let {
    username,
    files,
    folder = '',
    label = 'Download',
    compact = false,
  }: { username: string; files: FileRequest[]; folder?: string; label?: string; compact?: boolean } = $props();

  let busy = $state(false);
  let result = $state('');
  let failed = $state(false);

  async function go() {
    busy = true;
    result = '';
    failed = false;
    try {
      const queued = folder ? await api.downloadFolder(username, folder, files) : await api.download(username, files);
      result = compact ? '✓' : `Queued ${queued.length} file${queued.length === 1 ? '' : 's'}`;
    } catch (e) {
      failed = true;
      result = compact ? '!' : errorText(e);
    } finally {
      busy = false;
    }
  }
</script>

<span class="wrap">
  <button
    class:primary={!compact}
    class:icon={compact}
    disabled={busy}
    onclick={go}
    title={compact ? label : undefined}
    aria-label={compact ? label : undefined}
  >
    {compact ? '↓' : busy ? 'Queuing…' : label}
  </button>
  {#if result}
    <span class="result" class:error={failed} title={failed ? result : undefined} role="status">
      {result}
      {#if !failed && !compact}<a href={href(['downloads'])}>View</a>{/if}
    </span>
  {/if}
</span>

<style>
  .wrap {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .icon {
    padding: 0 7px;
    line-height: 20px;
    font-size: 13px;
  }
  .result {
    font-size: 12.5px;
    color: var(--good);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .result.error {
    color: var(--bad);
  }
</style>

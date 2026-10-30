<script lang="ts">
  // Queues files or a folder for download and says how it went.
  import { api, errorText, type FileRequest } from './api';
  import { href } from './router.svelte';
  import Icon from './Icon.svelte';

  let {
    username,
    files,
    folder = '',
    label = 'Download',
    compact = false,
  }: { username: string; files: FileRequest[]; folder?: string; label?: string; compact?: boolean } = $props();

  let busy = $state(false);
  let done = $state(0);
  let failure = $state('');

  async function go() {
    busy = true;
    failure = '';
    try {
      const queued = folder ? await api.downloadFolder(username, folder, files) : await api.download(username, files);
      done = queued.length;
    } catch (e) {
      failure = errorText(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if compact}
  <button
    class="icon-btn small"
    class:ok={done > 0}
    class:fail={!!failure}
    disabled={busy}
    onclick={go}
    title={failure || (done ? 'Queued' : label)}
    aria-label={label}
  >
    <Icon name={done ? 'check' : 'download'} size={17} />
  </button>
{:else}
  <div class="wrap">
    <button class="primary" disabled={busy} onclick={go}>
      <Icon name={done ? 'check' : 'download'} size={18} />
      {busy ? 'Queuing…' : done ? 'Queued' : label}
    </button>
    {#if done}
      <span class="result" role="status">
        {done} file{done === 1 ? '' : 's'} queued · <a href={href(['downloads'])}>View downloads</a>
      </span>
    {:else if failure}
      <span class="result error" role="alert">{failure}</span>
    {/if}
  </div>
{/if}

<style>
  .wrap {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .wrap button {
    width: 100%;
    min-height: 42px;
  }
  .result {
    font-size: 12.5px;
    color: var(--good);
    text-align: center;
  }
  .result.error {
    color: var(--bad);
  }
  .ok {
    color: var(--good);
  }
  .fail {
    color: var(--bad);
  }
</style>

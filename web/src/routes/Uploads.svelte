<script lang="ts">
  import { api, errorText, type Upload } from '../lib/api';
  import { href } from '../lib/router.svelte';
  import { isUploading, uploadSlots, uploads } from '../lib/uploads.svelte';
  import { plural, size, speed } from '../lib/format';

  let error = $state('');

  let all = $derived([...uploads.values()]);
  let active = $derived(all.filter(isUploading));
  // The server numbers queue places on load; new arrivals go last.
  let queued = $derived(
    all.filter((u) => u.state === 'queued').sort((a, b) => (a.place ?? 1e9) - (b.place ?? 1e9) || a.id - b.id),
  );
  let finished = $derived(all.filter((u) => !isUploading(u) && u.state !== 'queued').sort((a, b) => b.id - a.id));
  let rate = $derived(active.reduce((n, u) => n + u.speed, 0));

  async function run(action: () => Promise<unknown>) {
    error = '';
    try {
      await action();
    } catch (e) {
      error = errorText(e);
    }
  }

  const stateLabel: Record<Upload['state'], string> = {
    queued: 'Queued',
    initializing: 'Starting',
    transferring: 'Uploading',
    completed: 'Done',
    failed: 'Failed',
    cancelled: 'Cancelled',
  };
</script>

{#snippet row(u: Upload, i?: number)}
  <li>
    <div class="file">
      <span class="ellipsis" title={u.filename}>{u.name}</span>
      <span class="muted small ellipsis">
        <a href={href(['browse', u.username])}>{u.username}</a> ·
        {#if u.state === 'transferring'}
          {size(u.bytes)} of {size(u.size)} · {speed(u.speed)}
        {:else if u.state === 'queued' && i !== undefined}
          place {i + 1} · {size(u.size)}
        {:else if u.error}
          <span class:error={u.state === 'failed'}>{u.error}</span>
        {:else}
          {size(u.size)}
        {/if}
      </span>
      {#if u.state === 'transferring'}
        <div class="bar"><span style:width="{u.size ? (u.bytes / u.size) * 100 : 0}%"></span></div>
      {/if}
    </div>
    <span class="badge {u.state}">{stateLabel[u.state]}</span>
    <span class="actions">
      {#if u.state === 'queued' || isUploading(u)}
        <button class="ghost" title="Cancel" aria-label="Cancel {u.name}" onclick={() => run(() => api.cancelUpload(u.id))}>✕</button>
      {:else}
        <button class="ghost" title="Remove from list" aria-label="Remove {u.name}" onclick={() => run(() => api.removeUpload(u.id))}>🗑</button>
      {/if}
    </span>
  </li>
{/snippet}

<div class="page">
  <div class="toolbar">
    <div class="summary">
      {#if active.length}<span class="dot good pulse"></span>{/if}
      <strong>{active.length} of {uploadSlots.slots} slots in use</strong>
      <span class="muted">{rate > 0 ? speed(rate) + ' · ' : ''}{plural(queued.length, 'file')} queued</span>
    </div>
    <button disabled={!finished.length} onclick={() => run(() => api.clearUploads())}>Clear finished</button>
  </div>
  {#if error}<p class="error banner" role="alert">{error}</p>{/if}
  <div class="list">
    {#if active.length}
      <h2>Uploading</h2>
      <ul>{#each active as u (u.id)}{@render row(u)}{/each}</ul>
    {/if}
    {#if queued.length}
      <h2>Queued</h2>
      <ul>{#each queued as u, i (u.id)}{@render row(u, i)}{/each}</ul>
    {/if}
    {#if finished.length}
      <h2>Finished</h2>
      <ul>{#each finished as u (u.id)}{@render row(u)}{/each}</ul>
    {/if}
    {#if !all.length}
      <p class="hint muted">Nobody is downloading from you right now. Files you share are listed under <a href={href(['shares'])}>Shares</a>.</p>
    {/if}
  </div>
</div>

<style>
  .page {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  .small {
    font-size: 12.5px;
  }
  .toolbar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .summary {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .banner {
    margin: 10px 16px 0;
  }
  .list {
    flex: 1;
    overflow-y: auto;
    padding: 4px 16px 24px;
  }
  h2 {
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--muted);
    margin: 16px 0 6px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 10px;
    overflow: hidden;
  }
  li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
  }
  li + li {
    border-top: 1px solid var(--border);
  }
  .file {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
  }
  .bar {
    height: 2px;
    margin-top: 4px;
    background: var(--surface-2);
    border-radius: 2px;
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.4s ease;
  }
  .badge {
    font-size: 12px;
    padding: 2px 8px;
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--muted);
    white-space: nowrap;
  }
  .badge.transferring,
  .badge.initializing {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .badge.completed {
    color: var(--good);
  }
  .badge.failed {
    color: var(--bad);
  }
  .actions button {
    padding: 2px 8px;
    color: var(--muted);
  }
  .hint {
    padding: 12px 0;
  }
</style>

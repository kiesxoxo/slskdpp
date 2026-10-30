<script lang="ts">
  import { api, errorText, type Upload } from '../lib/api';
  import { href } from '../lib/router.svelte';
  import { isUploading, uploadSlots, uploads } from '../lib/uploads.svelte';
  import { plural, size, speed } from '../lib/format';
  import Icon from '../lib/Icon.svelte';
  import PageHead from '../lib/PageHead.svelte';

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
  const badge: Record<Upload['state'], string> = {
    queued: '',
    initializing: 'accent',
    transferring: 'accent',
    completed: 'good',
    failed: 'bad',
    cancelled: '',
  };
</script>

{#snippet row(u: Upload, place?: number)}
  <li>
    <div class="file">
      <span class="ellipsis fname" title={u.filename}>{u.name}</span>
      <span class="muted small ellipsis">
        <a href={href(['browse', u.username])}>{u.username}</a> ·
        {#if u.state === 'transferring'}
          {size(u.bytes)} of {size(u.size)} · {speed(u.speed)}
        {:else if place !== undefined}
          #{place} in queue · {size(u.size)}
        {:else if u.error}
          <span class:error={u.state === 'failed'}>{u.error}</span>
        {:else}
          {size(u.size)}
        {/if}
      </span>
      {#if u.state === 'transferring'}
        <div class="bar thin"><span style:width="{u.size ? (u.bytes / u.size) * 100 : 0}%"></span></div>
      {/if}
    </div>
    <span class="badge {badge[u.state]}">{stateLabel[u.state]}</span>
    {#if u.state === 'queued' || isUploading(u)}
      <button class="icon-btn small" title="Cancel" aria-label="Cancel {u.name}" onclick={() => run(() => api.cancelUpload(u.id))}><Icon name="close" size={17} /></button>
    {:else}
      <button class="icon-btn small" title="Remove from list" aria-label="Remove {u.name}" onclick={() => run(() => api.removeUpload(u.id))}><Icon name="trash" size={17} /></button>
    {/if}
  </li>
{/snippet}

<div class="page-scroll">
  <div class="page-body">
    <PageHead title="Uploads">
      {#snippet summary()}
        <span class="slots" aria-label="{active.length} of {uploadSlots.slots} upload slots in use">
          {#each { length: Math.max(uploadSlots.slots, 1) } as _, i (i)}
            <span class="slot" class:used={i < active.length}></span>
          {/each}
        </span>
        <span>
          <strong class="strong">{active.length}/{uploadSlots.slots}</strong> slots{rate > 0 ? ' · ' + speed(rate) : ''} · {plural(queued.length, 'file')} queued
        </span>
      {/snippet}
      {#snippet actions()}
        <button class="small" disabled={!finished.length} onclick={() => run(() => api.clearUploads())}><Icon name="check" size={16} /> Clear finished</button>
      {/snippet}
    </PageHead>
    {#if error}<p class="banner" role="alert">{error}</p>{/if}

    {#if active.length}
      <section>
        <h2 class="section-title">Uploading</h2>
        <ul class="rows card">{#each active as u (u.id)}{@render row(u)}{/each}</ul>
      </section>
    {/if}
    {#if queued.length}
      <section>
        <h2 class="section-title">Queued</h2>
        <ul class="rows card">{#each queued as u, i (u.id)}{@render row(u, i + 1)}{/each}</ul>
      </section>
    {/if}
    {#if finished.length}
      <section>
        <h2 class="section-title">Finished</h2>
        <ul class="rows card">{#each finished as u (u.id)}{@render row(u)}{/each}</ul>
      </section>
    {/if}
    {#if !all.length}
      <div class="card">
        <div class="empty">
          <span class="empty-icon"><Icon name="upload" size={26} /></span>
          <strong>Nobody is downloading from you</strong>
          <p>People who find your files queue them here. Manage what you share under <a href={href(['shares'])}>Shares</a>.</p>
        </div>
      </div>
    {/if}
  </div>
</div>

<style>
  .strong {
    color: var(--text);
  }
  section {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .slots {
    display: inline-flex;
    gap: 3px;
  }
  .slot {
    width: 10px;
    height: 14px;
    border-radius: 3px;
    background: var(--surface-3);
  }
  .slot.used {
    background: var(--accent);
  }
  ul.rows {
    overflow: hidden;
  }
  ul.rows > li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 10px 10px 16px;
  }
  .file {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1;
    min-width: 0;
  }
  .fname {
    font-weight: 500;
  }
  .bar.thin {
    height: 3px;
    margin-top: 4px;
  }
</style>

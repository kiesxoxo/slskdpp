<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorText, type SearchFilter, type WishlistEntry } from '../lib/api';
  import { subscribe } from '../lib/events';
  import { ago, plural } from '../lib/format';
  import { href } from '../lib/router.svelte';
  import Icon from '../lib/Icon.svelte';
  import PageHead from '../lib/PageHead.svelte';

  let entries = $state<WishlistEntry[]>([]);
  let interval = $state(0);
  let error = $state('');

  let query = $state('');
  let formats = $state('');
  let minBitrate = $state(0);
  let minFiles = $state(0);
  let freeOnly = $state(false);
  let auto = $state(true);
  let adding = $state(false);

  async function load() {
    try {
      const r = await api.wishlist();
      entries = r.entries;
      interval = r.interval_seconds;
    } catch (e) {
      error = errorText(e);
    }
  }

  onMount(() => {
    load();
    return subscribe(0, {
      wishlist: (e: WishlistEntry) => {
        const i = entries.findIndex((x) => x.id === e.id);
        if (i >= 0) entries[i] = e;
        else entries.push(e);
      },
    });
  });

  async function run(action: () => Promise<unknown>) {
    error = '';
    try {
      await action();
      await load();
    } catch (e) {
      error = errorText(e);
    }
  }

  async function add(e: SubmitEvent) {
    e.preventDefault();
    const filter: SearchFilter = {
      formats: formats.split(',').map((s) => s.trim().toLowerCase()).filter(Boolean),
      min_bitrate: minBitrate,
      min_files: minFiles || 0,
      free_slot_only: freeOnly,
    };
    adding = true;
    await run(async () => {
      await api.addWishlist(query, filter, auto);
      query = '';
    });
    adding = false;
  }

  function describe(f: SearchFilter): string[] {
    const out: string[] = [];
    if (f.formats?.length) out.push(f.formats.join('/'));
    if (f.min_bitrate) out.push(`≥ ${f.min_bitrate} kbps`);
    if (f.min_files) out.push(`≥ ${f.min_files} files`);
    if (f.free_slot_only) out.push('free slot');
    return out;
  }

  let sorted = $derived([...entries].sort((a, b) => Number(b.enabled) - Number(a.enabled) || b.id - a.id));
</script>

<div class="page-scroll">
  <div class="page-body">
    <PageHead title="Wishlist">
      {#snippet summary()}
        <span>Searches that keep running{interval ? `, one every ${interval < 90 ? interval + ' s' : Math.round(interval / 60) + ' min'}` : ''}</span>
      {/snippet}
    </PageHead>

    <form class="add card" onsubmit={add}>
      <div class="row">
        <label class="field">
          <Icon name="search" size={18} />
          <input placeholder="Artist and album you are after" bind:value={query} aria-label="Wishlist query" />
        </label>
        <button class="primary" disabled={adding || !query.trim()}><Icon name="plus" size={18} /> Add</button>
      </div>
      <div class="row filters">
        <input class="formats" placeholder="Formats, e.g. flac" bind:value={formats} aria-label="Formats" />
        <select bind:value={minBitrate} aria-label="Minimum bitrate">
          <option value={0}>Any bitrate</option>
          <option value={256}>≥ 256 kbps</option>
          <option value={320}>≥ 320 kbps</option>
        </select>
        <label class="check">Min files <input type="number" min="0" inputmode="numeric" class="narrow" bind:value={minFiles} /></label>
        <label class="check"><input type="checkbox" bind:checked={freeOnly} /> Free slot</label>
        <label class="check auto"><input type="checkbox" bind:checked={auto} /> Download the best match</label>
      </div>
    </form>
    {#if error}<p class="banner" role="alert">{error}</p>{/if}

    {#if sorted.length}
      <ul class="rows card entries">
        {#each sorted as e (e.id)}
          <li class:off={!e.enabled}>
            <div class="info">
              <span class="query">
                {#if e.running}<span class="dot good pulse" title="Searching"></span>{/if}
                {e.query}
              </span>
              <span class="chips">
                {#each describe(e.filter) as c (c)}<span class="badge">{c}</span>{/each}
                {#if e.auto_download}<span class="badge accent">Auto-download</span>{/if}
              </span>
              <span class="muted small">
                {#if e.got_folder}
                  <span class="got"><Icon name="check" size={14} /> Downloaded {e.got_folder.split('\\').pop()} from {e.got_user} {ago(e.got_at)}</span>
                  · <a href={href(['downloads'])}>Downloads</a>
                {:else if e.running}
                  Searching…
                {:else}
                  {e.last_run ? `Searched ${ago(e.last_run)} · ${plural(e.matches, 'matching album')}` : 'Not searched yet'}
                  {#if e.last_search}· <a href={href(['search', String(e.last_search)])}>See results</a>{/if}
                {/if}
              </span>
            </div>
            <div class="actions">
              <label class="toggle" title={e.enabled ? 'Searching on schedule' : 'Paused'}>
                <input type="checkbox" checked={e.enabled} onchange={() => run(() => api.updateWishlist(e.id, { enabled: !e.enabled }))} />
                <span class="track"><span class="knob"></span></span>
                <span class="sr">Search on schedule</span>
              </label>
              <button class="small" disabled={e.running} onclick={() => run(() => api.searchWishlist(e.id))}>Search now</button>
              <button class="icon-btn small" aria-label="Remove {e.query}" title="Remove" onclick={() => run(() => api.removeWishlist(e.id))}><Icon name="trash" size={17} /></button>
            </div>
          </li>
        {/each}
      </ul>
    {:else}
      <div class="card">
        <div class="empty">
          <span class="empty-icon"><Icon name="wishlist" size={26} /></span>
          <strong>Your wishlist is empty</strong>
          <p>Add things you could not find; slskdpp keeps looking and can grab the best copy when it shows up.</p>
        </div>
      </div>
    {/if}
  </div>
</div>

<style>
  .add {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 14px;
  }
  .row {
    display: flex;
    gap: 8px;
  }
  .field {
    position: relative;
    flex: 1;
    display: flex;
    align-items: center;
    color: var(--faint);
  }
  .field :global(.icon) {
    position: absolute;
    left: 12px;
    pointer-events: none;
  }
  .field input {
    width: 100%;
    padding-left: 38px;
    color: var(--text);
  }
  .filters {
    flex-wrap: wrap;
    align-items: center;
  }
  .filters input:not([type='checkbox']),
  .filters select {
    min-height: 32px;
    padding: 4px 10px;
    font-size: 13px;
  }
  .formats {
    width: 150px;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    color: var(--muted);
  }
  .check.auto {
    color: var(--text);
    font-weight: 500;
  }
  .narrow {
    width: 64px;
  }
  .entries {
    overflow: hidden;
  }
  .entries > li {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 14px 14px 14px 16px;
  }
  .info {
    display: flex;
    flex-direction: column;
    gap: 5px;
    flex: 1;
    min-width: 0;
  }
  .query {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 600;
    font-size: 15px;
  }
  li.off .query {
    color: var(--muted);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .chips:empty {
    display: none;
  }
  .got {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--good);
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .toggle {
    position: relative;
    display: inline-flex;
    cursor: pointer;
  }
  .toggle input {
    position: absolute;
    opacity: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    cursor: pointer;
  }
  .track {
    width: 38px;
    height: 22px;
    border-radius: 999px;
    background: var(--surface-3);
    padding: 2px;
    transition: background 0.15s;
  }
  .knob {
    display: block;
    width: 18px;
    height: 18px;
    border-radius: 50%;
    background: #fff;
    box-shadow: var(--shadow-sm);
    transition: transform 0.15s;
  }
  .toggle input:checked + .track {
    background: var(--accent);
  }
  .toggle input:checked + .track .knob {
    transform: translateX(16px);
  }
  .toggle input:focus-visible + .track {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
  }
  @media (max-width: 720px) {
    .filters input:not([type='checkbox']),
    .filters select {
      min-height: 40px;
      font-size: 16px;
    }
    .formats {
      flex: 1 1 100%;
    }
    .entries > li {
      flex-wrap: wrap;
    }
    .actions {
      width: 100%;
      justify-content: flex-end;
    }
  }
</style>

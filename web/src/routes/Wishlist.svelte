<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorText, type SearchFilter, type WishlistEntry } from '../lib/api';
  import { subscribe } from '../lib/events';
  import { ago, plural } from '../lib/format';
  import { href } from '../lib/router.svelte';

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

<div class="page">
  <div class="content">
    <form class="add" onsubmit={add}>
      <h2>Add to wishlist</h2>
      <div class="row">
        <input placeholder="Artist album…" bind:value={query} aria-label="Wishlist query" />
        <button class="primary" disabled={adding || !query.trim()}>Add</button>
      </div>
      <div class="row filters">
        <input class="formats" placeholder="formats: flac, mp3" bind:value={formats} aria-label="Formats" />
        <select bind:value={minBitrate} aria-label="Minimum bitrate">
          <option value={0}>any bitrate</option>
          <option value={256}>≥ 256 kbps</option>
          <option value={320}>≥ 320 kbps</option>
        </select>
        <label class="check">min files <input type="number" min="0" class="narrow" bind:value={minFiles} /></label>
        <label class="check"><input type="checkbox" bind:checked={freeOnly} /> free slot</label>
        <label class="check"><input type="checkbox" bind:checked={auto} /> download the best match</label>
      </div>
      <p class="muted small">
        Entries are searched one at a time{interval ? `, one every ${interval < 90 ? interval + ' s' : Math.round(interval / 60) + ' min'}` : ''}, the
        pace the server allows.
      </p>
    </form>
    {#if error}<p class="error" role="alert">{error}</p>{/if}

    <ul class="entries">
      {#each sorted as e (e.id)}
        <li class:off={!e.enabled}>
          <div class="info">
            <span class="query">
              {#if e.running}<span class="dot good pulse" title="Searching"></span>{/if}
              {e.query}
            </span>
            <span class="chips">
              {#each describe(e.filter) as c (c)}<span class="chip">{c}</span>{/each}
              {#if e.auto_download}<span class="chip on">auto-download</span>{/if}
            </span>
            <span class="muted small">
              {#if e.got_folder}
                Downloaded <span class="mono">{e.got_folder.split('\\').pop()}</span> from {e.got_user} {ago(e.got_at)} ·
                <a href={href(['downloads'])}>Downloads</a>
              {:else if e.running}
                Searching…
              {:else}
                Searched {ago(e.last_run)}{e.last_run ? ` · ${plural(e.matches, 'matching album')}` : ''}
                {#if e.last_search}· <a href={href(['search', String(e.last_search)])}>results</a>{/if}
              {/if}
            </span>
          </div>
          <div class="actions">
            <label class="check" title="Search on the schedule">
              <input type="checkbox" checked={e.enabled} onchange={() => run(() => api.updateWishlist(e.id, { enabled: !e.enabled }))} />
              on
            </label>
            <button disabled={e.running} onclick={() => run(() => api.searchWishlist(e.id))}>Search now</button>
            <button class="ghost" aria-label="Remove {e.query}" title="Remove" onclick={() => run(() => api.removeWishlist(e.id))}>🗑</button>
          </div>
        </li>
      {:else}
        <li class="muted">Nothing on the wishlist. Add things you could not find; slskdpp keeps looking.</li>
      {/each}
    </ul>
  </div>
</div>

<style>
  .page {
    height: 100%;
    overflow-y: auto;
  }
  .content {
    padding: 16px;
    max-width: 900px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .small {
    font-size: 12.5px;
  }
  h2 {
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--muted);
    margin: 0;
  }
  .add {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .add p {
    margin: 0;
  }
  .row {
    display: flex;
    gap: 6px;
  }
  .row > input:first-child {
    flex: 1;
  }
  .filters {
    flex-wrap: wrap;
    align-items: center;
  }
  .filters input,
  .filters select {
    padding: 4px 8px;
    font-size: 13px;
  }
  .filters .formats {
    flex: 0 1 180px;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 13px;
    color: var(--muted);
  }
  .narrow {
    width: 56px;
  }
  .entries {
    list-style: none;
    margin: 0;
    padding: 0;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 10px;
  }
  .entries > li {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 14px;
  }
  .entries > li + li {
    border-top: 1px solid var(--border);
  }
  .entries > li.off .query {
    color: var(--muted);
  }
  .info {
    display: flex;
    flex-direction: column;
    gap: 3px;
    flex: 1;
    min-width: 0;
  }
  .query {
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .chips:empty {
    display: none;
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .actions button {
    padding: 4px 10px;
    font-size: 13px;
  }
  @media (max-width: 600px) {
    .entries > li {
      flex-wrap: wrap;
    }
    .actions {
      width: 100%;
      justify-content: flex-end;
    }
  }
</style>

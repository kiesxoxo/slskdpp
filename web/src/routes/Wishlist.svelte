<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorText, type SearchFilter, type WishlistEntry } from '../lib/api';
  import { emptyFilter, toSearchFilter, type QualityFilter } from '../lib/albums';
  import { toast } from '../lib/toast.svelte';
  import { subscribe } from '../lib/events';
  import { ago, plural } from '../lib/format';
  import { href } from '../lib/router.svelte';
  import Icon from '../lib/Icon.svelte';
  import PageHead from '../lib/PageHead.svelte';

  let entries = $state<WishlistEntry[]>([]);
  let interval = $state(0);
  let error = $state('');

  let query = $state('');
  let formats = $state<string[]>([]);
  let quality = $state<QualityFilter>('any');
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
    const filter: SearchFilter = toSearchFilter({ ...emptyFilter(), formats, quality, minFiles: minFiles || 0, freeOnly });
    adding = true;
    const q = query;
    await run(async () => {
      await api.addWishlist(q, filter, auto);
      query = '';
      toast({ kind: 'success', message: `“${q}” added; the first search runs shortly` });
    });
    adding = false;
  }

  const formatChoices = ['flac', 'mp3', 'm4a', 'ogg', 'opus', 'wav'];
  function toggleFormat(f: string) {
    formats = formats.includes(f) ? formats.filter((x) => x !== f) : [...formats, f];
  }

  function describe(f: SearchFilter): string[] {
    const out: string[] = [];
    if (f.formats?.length) out.push(f.formats.map((x) => x.toUpperCase()).join(' / '));
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
        <div class="chips" role="group" aria-label="Formats">
          {#each formatChoices as f (f)}
            <button type="button" class="chip" class:on={formats.includes(f)} aria-pressed={formats.includes(f)} onclick={() => toggleFormat(f)}>{f.toUpperCase()}</button>
          {/each}
        </div>
        <select bind:value={quality} aria-label="Quality">
          <option value="any">Any quality</option>
          <option value="lossless">Lossless</option>
          <option value="320">320 kbps or better</option>
          <option value="256">256 kbps or better</option>
          <option value="192">192 kbps or better</option>
        </select>
        <button type="button" class="chip" class:on={freeOnly} aria-pressed={freeOnly} onclick={() => (freeOnly = !freeOnly)}>
          <span class="dot" class:good={freeOnly}></span> Free slot
        </button>
        <label class="check">Min tracks <input type="number" min="0" inputmode="numeric" class="narrow" bind:value={minFiles} /></label>
        <label class="toggle auto">
          <input type="checkbox" bind:checked={auto} />
          <span class="track"><span class="knob"></span></span>
          <span class="label">Download the best match</span>
        </label>
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
              <span class="tags">
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
                <span class="sr-only">Search on schedule</span>
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
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .auto {
    margin-left: auto;
    color: var(--text);
    font-weight: 500;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    color: var(--muted);
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
  .tags {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .tags:empty {
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
  @media (max-width: 720px) {
    .filters input:not([type='checkbox']),
    .filters select {
      min-height: 40px;
      font-size: 16px;
    }
    .auto {
      margin-left: 0;
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

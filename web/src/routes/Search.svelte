<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorText, type Hit, type SearchView } from '../lib/api';
  import { groupAlbums, hitKey, matches, rank, type Album, type Filter } from '../lib/albums';
  import { subscribe } from '../lib/events';
  import { duration, plural, quality, size, speed } from '../lib/format';
  import { href, navigate } from '../lib/router.svelte';
  import { session } from '../lib/session.svelte';
  import VirtualList from '../lib/VirtualList.svelte';
  import DownloadButton from '../lib/DownloadButton.svelte';
  import Icon from '../lib/Icon.svelte';

  let { id }: { id: number | null } = $props();

  let query = $state('');
  let searches = $state<SearchView[]>([]);
  let view = $state<SearchView | null>(null);
  let error = $state('');
  let starting = $state(false);

  // Hits stream in fast; collect them and publish in batches.
  let hits = $state.raw<Hit[]>([]);
  const seen = new Set<string>();
  let pending: Hit[] = [];
  let flushTimer: ReturnType<typeof setTimeout> | undefined;

  function addHits(list: Hit[]) {
    for (const h of list) {
      const k = hitKey(h);
      if (!seen.has(k)) {
        seen.add(k);
        pending.push(h);
      }
    }
    flushTimer ??= setTimeout(() => {
      flushTimer = undefined;
      hits = hits.concat(pending);
      pending = [];
    }, 150);
  }

  // Filters.
  let formats = $state<string[]>([]);
  let minBitrate = $state(0);
  let freeOnly = $state(false);
  let minFiles = $state(0);
  let excludeText = $state('');
  let showFilters = $state(false);

  let filter: Filter = $derived({
    formats,
    minBitrate,
    freeOnly,
    minFiles: minFiles || 0,
    exclude: excludeText
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean),
  });
  let activeFilters = $derived(
    formats.length + Number(minBitrate > 0) + Number(freeOnly) + Number(minFiles > 0) + Number(filter.exclude.length > 0),
  );
  let albums = $derived(groupAlbums(rank(hits.filter((h) => matches(h, filter)))));
  let users = $derived(new Set(hits.map((h) => h.user)).size);
  let fileCount = $derived(hits.reduce((n, h) => n + h.file_count, 0));
  // The most common formats in the results, offered as filter chips.
  let formatChoices = $derived.by(() => {
    const counts = new Map<string, number>();
    for (const h of hits) for (const f of h.formats) counts.set(f, (counts.get(f) ?? 0) + 1);
    const top = [...counts].sort((a, b) => b[1] - a[1]).slice(0, 8).map(([f]) => f);
    return [...new Set([...formats, ...top])];
  });

  // The selection follows a hit, so it survives albums reordering.
  let selectedHit = $state<string | null>(null);
  let selected = $derived(
    selectedHit ? (albums.find((a) => a.sources.some((s) => hitKey(s) === selectedHit)) ?? null) : null,
  );
  let source = $derived(selected?.sources.find((s) => hitKey(s) === selectedHit) ?? selected?.sources[0] ?? null);

  let active = $state(false);

  async function loadSearches() {
    try {
      searches = await api.searches();
    } catch {
      // the list is a convenience
    }
  }

  async function loadResults(searchId: number) {
    try {
      const [v, list] = await Promise.all([api.search(searchId), api.hits(searchId)]);
      view = v;
      active = v.active;
      query = v.query;
      addHits(list);
    } catch (e) {
      error = errorText(e);
    }
  }

  onMount(() => {
    loadSearches();
    if (id === null) return;
    const searchId = id;
    // Subscribe before fetching so nothing falls between the two; the
    // seen set drops duplicates.
    const stop = subscribe(
      searchId,
      {
        'search.hit': (e: { hit: Hit }) => addHits([e.hit]),
        'search.done': () => {
          active = false;
          loadSearches();
        },
      },
      () => loadResults(searchId),
    );
    loadResults(searchId);
    return () => {
      stop();
      clearTimeout(flushTimer);
    };
  });

  async function start(e: SubmitEvent) {
    e.preventDefault();
    if (!query.trim()) return;
    starting = true;
    error = '';
    try {
      const v = await api.startSearch(query);
      navigate(['search', String(v.id)]);
    } catch (err) {
      error = errorText(err);
    } finally {
      starting = false;
    }
  }

  async function remove(s: SearchView) {
    await api.deleteSearch(s.id).catch(() => {});
    if (s.id === id) navigate(['search']);
    else loadSearches();
  }

  function toggleFormat(f: string) {
    formats = formats.includes(f) ? formats.filter((x) => x !== f) : [...formats, f];
  }

  function clearFilters() {
    formats = [];
    minBitrate = 0;
    freeOnly = false;
    minFiles = 0;
    excludeText = '';
  }

  function select(a: Album) {
    selectedHit = hitKey(a.sources[0]);
  }

  let wished = $state(false);
  async function addToWishlist() {
    if (!view) return;
    try {
      await api.addWishlist(view.query, { formats, min_bitrate: minBitrate, min_files: minFiles || 0, free_slot_only: freeOnly }, false);
      wished = true;
    } catch (e) {
      error = errorText(e);
    }
  }

  function slotLabel(h: Hit): string {
    return h.slot_free ? 'Free slot' : `Queue ${h.queue_length}`;
  }

  function fileQuality(f: Hit['files'][number]): string {
    if (f.bit_depth && f.sample_rate) return `${f.bit_depth}/${f.sample_rate / 1000}k`;
    return f.bitrate ? `${f.bitrate}k` : '';
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && selected) selectedHit = null;
  }
</script>

<svelte:window onkeydown={keydown} />

<div class="page" class:has-details={selected}>
  <section class="results">
    <div class="head">
      <form class="search-form" onsubmit={start}>
        <label class="field">
          <Icon name="search" size={18} />
          <input
            type="search"
            enterkeyhint="search"
            placeholder={session.connected ? 'Artist, album or track' : 'Not connected to the Soulseek server'}
            bind:value={query}
            aria-label="Search query"
            disabled={!session.connected}
          />
        </label>
        <button class="primary" disabled={starting || !session.connected || !query.trim()}>Search</button>
      </form>
      {#if searches.length}
        <div class="recent" aria-label="Recent searches">
          {#each searches as s (s.id)}
            <span class="recent-chip" class:current={s.id === id}>
              <a href={href(['search', String(s.id)])} title={s.query}>
                {#if s.id === id ? active : s.active}<span class="dot good pulse"></span>{/if}
                <span class="ellipsis">{s.query}</span>
                <span class="n">{s.id === id ? hits.length : s.folders}</span>
              </a>
              <button class="x" title="Remove" aria-label="Remove search {s.query}" onclick={() => remove(s)}>
                <Icon name="close" size={14} stroke={2.2} />
              </button>
            </span>
          {/each}
        </div>
      {/if}
    </div>

    {#if error}<p class="banner err" role="alert">{error}</p>{/if}

    {#if id === null}
      <div class="empty">
        <span class="empty-icon"><Icon name="search" size={26} /></span>
        <strong>Find music on Soulseek</strong>
        <p>Results stream in as people answer, grouped into albums with the best sources first.</p>
      </div>
    {:else}
      <div class="toolbar">
        <div class="summary">
          {#if active}<span class="dot good pulse" title="Collecting results"></span>{/if}
          <strong>{plural(albums.length, 'album')}</strong>
          <span class="muted ellipsis">
            {plural(users, 'user')} · {plural(fileCount, 'file')}{active ? ' · searching…' : ''}
          </span>
        </div>
        <div class="actions">
          <button class="small filter-toggle" class:on={showFilters} onclick={() => (showFilters = !showFilters)} aria-expanded={showFilters}>
            <Icon name="filter" size={16} /> Filters
            {#if activeFilters}<span class="n">{activeFilters}</span>{/if}
          </button>
          {#if wished}
            <a class="small-link" href={href(['wishlist'])}><Icon name="check" size={16} /> On wishlist</a>
          {:else}
            <button class="small" onclick={addToWishlist} title="Keep searching for this on a schedule">
              <Icon name="wishlist" size={16} /> <span class="hide-xs">Wishlist</span>
            </button>
          {/if}
        </div>
      </div>
      <div class="filters" class:open={showFilters}>
        <div class="chips">
          {#each formatChoices as f (f)}
            <button class="chip" class:on={formats.includes(f)} aria-pressed={formats.includes(f)} onclick={() => toggleFormat(f)}>{f}</button>
          {/each}
        </div>
        <select bind:value={minBitrate} aria-label="Minimum bitrate">
          <option value={0}>Any bitrate</option>
          <option value={192}>≥ 192 kbps</option>
          <option value={256}>≥ 256 kbps</option>
          <option value={320}>≥ 320 kbps</option>
        </select>
        <label class="check"><input type="checkbox" bind:checked={freeOnly} /> Free slot</label>
        <label class="check">Min files <input type="number" min="0" inputmode="numeric" class="narrow" bind:value={minFiles} /></label>
        <input class="exclude" placeholder="Exclude users" bind:value={excludeText} aria-label="Exclude users (comma-separated)" />
        {#if activeFilters}<button class="ghost small" onclick={clearFilters}>Clear</button>{/if}
      </div>
      <div class="list">
        <VirtualList items={albums} rowHeight={68}>
          {#snippet row(a: Album)}
            {@const best = a.sources[0]}
            <button class="album" class:selected={selected?.key === a.key} onclick={() => select(a)}>
              <span class="fmt" class:lossless={best.lossless}>{best.formats[0] ?? '?'}</span>
              <span class="main">
                <span class="name ellipsis">{a.name}</span>
                <span class="meta ellipsis">
                  {plural(a.file_count, 'file')} · {quality(best)} · {size(a.total_size)}
                </span>
              </span>
              <span class="side">
                <span class="who ellipsis">{best.user}{#if a.sources.length > 1}<span class="plus">+{a.sources.length - 1}</span>{/if}</span>
                <span class="slot" class:free={best.slot_free}>{slotLabel(best)}</span>
              </span>
            </button>
          {/snippet}
          {#snippet empty()}
            <div class="empty">
              {#if active}
                <span class="empty-icon pulse"><Icon name="signal" size={26} /></span>
                <strong>Listening for answers…</strong>
                <p>Peers answer over the next minute or two.</p>
              {:else if hits.length}
                <span class="empty-icon"><Icon name="filter" size={26} /></span>
                <strong>Nothing matches the filters</strong>
                <button class="small" onclick={clearFilters}>Clear filters</button>
              {:else}
                <span class="empty-icon"><Icon name="search" size={26} /></span>
                <strong>No results</strong>
                <p>Try fewer or different words.</p>
              {/if}
            </div>
          {/snippet}
        </VirtualList>
      </div>
    {/if}
  </section>

  {#if selected && source}
    <section class="details" aria-label="Album details">
      <div class="details-head">
        <button class="icon-btn back" aria-label="Back to results" onclick={() => (selectedHit = null)}><Icon name="back" /></button>
        <div class="title">
          <h2 class="ellipsis" title={selected.name}>{selected.name}</h2>
          <p class="muted small">{plural(selected.file_count, 'file')} · {size(selected.total_size)} · {plural(selected.sources.length, 'source')}</p>
        </div>
        <button class="icon-btn close" aria-label="Close details" onclick={() => (selectedHit = null)}><Icon name="close" /></button>
      </div>
      <div class="details-body">
        <div class="section-title">Sources</div>
        <ul class="sources">
          {#each selected.sources as s (hitKey(s))}
            <li>
              <button class="source" class:on={s === source} onclick={() => (selectedHit = hitKey(s))}>
                <span class="who ellipsis"><Icon name="user" size={16} /> {s.user}</span>
                <span class="src-meta">
                  <span class="slot" class:free={s.slot_free}>{slotLabel(s)}</span>
                  <span class="muted small">{speed(s.upload_speed)} · {quality(s)}</span>
                </span>
              </button>
            </li>
          {/each}
        </ul>
        <div class="folder">
          <span class="mono ellipsis" title={source.folder}>{source.folder}</span>
          <a href={href(['browse', source.user], { folder: source.folder })}>Browse user</a>
        </div>
        <table class="files">
          <thead>
            <tr><th>File</th><th class="num">Size</th><th class="num opt">Quality</th><th class="num len">Length</th><th class="act"></th></tr>
          </thead>
          <tbody>
            {#each source.files as f (f.path)}
              <tr>
                <td class="fcell" title={f.name}>
                  <span class="ellipsis fname">{f.name}</span>
                  {#if fileQuality(f) || f.duration}
                    <span class="fsub">{[fileQuality(f), duration(f.duration)].filter(Boolean).join(' · ')}</span>
                  {/if}
                </td>
                <td class="num">{size(f.size)}</td>
                <td class="num opt">{fileQuality(f)}</td>
                <td class="num len">{duration(f.duration)}</td>
                <td class="act">
                  <DownloadButton compact username={source.user} files={[{ path: f.path, size: f.size }]} label="Download {f.name}" />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#key hitKey(source)}
        <div class="details-foot">
          <DownloadButton
            username={source.user}
            folder={source.folder}
            files={source.files.map((f) => ({ path: f.path, size: f.size }))}
            label="Download folder"
          />
        </div>
      {/key}
    </section>
  {/if}
</div>

<style>
  .page {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: minmax(0, 1fr);
    height: 100%;
  }
  .page.has-details {
    grid-template-columns: minmax(0, 1fr) minmax(360px, 42%);
  }
  .results {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }
  .head {
    padding: 18px 20px 12px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .search-form {
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
    min-height: 42px;
    font-size: 15px;
    border-radius: 11px;
    color: var(--text);
  }
  .search-form button {
    min-height: 42px;
    padding: 0 20px;
    border-radius: 11px;
  }
  .recent {
    display: flex;
    gap: 6px;
    overflow-x: auto;
    scrollbar-width: none;
    margin: 0 -20px;
    padding: 0 20px;
  }
  .recent::-webkit-scrollbar {
    display: none;
  }
  .recent-chip {
    display: inline-flex;
    align-items: center;
    flex: none;
    max-width: 240px;
    border: 1px solid var(--border);
    background: var(--surface);
    border-radius: 999px;
    font-size: 13px;
  }
  .recent-chip.current {
    border-color: transparent;
    background: var(--accent-soft);
  }
  .recent-chip a {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    padding: 5px 4px 5px 12px;
    color: var(--text);
    font-weight: 500;
  }
  .recent-chip.current a {
    color: var(--accent-soft-text);
  }
  .recent-chip a:hover {
    text-decoration: none;
  }
  .n {
    font-size: 11.5px;
    font-weight: 600;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .recent-chip .x {
    min-height: 0;
    width: 26px;
    height: 26px;
    padding: 0;
    margin-right: 2px;
    border: 0;
    border-radius: 999px;
    background: transparent;
    color: var(--faint);
  }
  .recent-chip .x:hover {
    color: var(--text);
  }
  .err {
    margin: 0 20px 8px;
  }
  .toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 4px 20px 10px;
  }
  .summary {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .summary strong {
    white-space: nowrap;
  }
  .actions {
    display: flex;
    gap: 6px;
    flex: none;
  }
  .filter-toggle {
    display: none;
  }
  .filter-toggle .n {
    min-width: 18px;
    line-height: 18px;
    border-radius: 999px;
    background: var(--accent);
    color: var(--accent-text);
  }
  .small-link {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 13px;
    font-weight: 500;
    color: var(--good);
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    padding: 0 20px 12px;
    border-bottom: 1px solid var(--border);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .chips:empty {
    display: none;
  }
  .filters select,
  .filters input:not([type='checkbox']) {
    min-height: 32px;
    padding: 4px 10px;
    font-size: 13px;
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
  .exclude {
    width: 150px;
  }
  .list {
    flex: 1;
    min-height: 0;
  }
  .album {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    height: 100%;
    min-height: 0;
    text-align: left;
    border: 0;
    border-bottom: 1px solid var(--border);
    border-radius: 0;
    background: transparent;
    padding: 8px 20px;
    font-weight: 400;
  }
  .album:hover:not(:disabled) {
    background: var(--surface-2);
  }
  .album.selected {
    background: var(--accent-soft);
  }
  .fmt {
    display: grid;
    place-items: center;
    flex: none;
    width: 44px;
    height: 44px;
    border-radius: 10px;
    background: var(--surface-2);
    color: var(--muted);
    font-size: 11px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .fmt.lossless {
    background: var(--accent-soft);
    color: var(--accent-soft-text);
  }
  .main,
  .side {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .main {
    flex: 1;
  }
  .side {
    align-items: flex-end;
    max-width: 38%;
  }
  .name {
    font-weight: 600;
  }
  .meta {
    font-size: 12.5px;
    color: var(--muted);
  }
  .who {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-weight: 500;
  }
  .plus {
    margin-left: 4px;
    font-size: 11.5px;
    color: var(--muted);
  }
  .slot {
    font-size: 11.5px;
    font-weight: 600;
    color: var(--warn);
  }
  .slot.free {
    color: var(--good);
  }

  /* Details panel. */
  .details {
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
    background: var(--surface);
    border-left: 1px solid var(--border);
  }
  .details-head {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 14px 12px 12px 20px;
    border-bottom: 1px solid var(--border);
  }
  .details-head .title {
    flex: 1;
    min-width: 0;
  }
  .details-head h2 {
    font-size: 17px;
    letter-spacing: -0.01em;
  }
  .back {
    display: none;
  }
  .details-body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 14px 20px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .sources {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .source {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 8px 10px;
    border: 1px solid var(--border);
    font-weight: 400;
  }
  .source.on {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .src-meta {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
  }
  .folder {
    display: flex;
    gap: 10px;
    justify-content: space-between;
    align-items: baseline;
    padding-top: 6px;
  }
  .folder a {
    white-space: nowrap;
    font-size: 13px;
  }
  .files th:first-child {
    width: auto;
  }
  .fcell {
    overflow: hidden;
  }
  .fname {
    display: block;
  }
  .fsub {
    display: none;
    font-size: 12px;
    color: var(--muted);
  }
  th.act {
    width: 46px;
  }
  td.act {
    text-align: right;
  }
  .details-foot {
    padding: 12px 20px;
    border-top: 1px solid var(--border);
    background: var(--surface);
  }
  .details-foot :global(.wrap) {
    width: 100%;
  }

  @media (max-width: 1100px) {
    .page.has-details {
      grid-template-columns: minmax(0, 1fr) minmax(340px, 48%);
    }
  }

  /* Phones and narrow tablets: details become a full-screen sheet. */
  @media (max-width: 860px) {
    .page.has-details {
      grid-template-columns: minmax(0, 1fr);
    }
    .details {
      position: fixed;
      inset: 0;
      z-index: 30;
      border-left: 0;
      animation: rise 0.2s ease;
    }
    .details-head {
      padding: calc(8px + env(safe-area-inset-top)) 12px 10px 6px;
    }
    .back {
      display: inline-flex;
    }
    .close {
      display: none;
    }
    .details-foot {
      padding-bottom: calc(12px + env(safe-area-inset-bottom));
    }
  }

  @media (max-width: 720px) {
    .head {
      padding: 12px 14px 10px;
    }
    .recent {
      margin: 0 -14px;
      padding: 0 14px;
    }
    .toolbar {
      padding: 2px 14px 10px;
    }
    .err {
      margin: 0 14px 8px;
    }
    .filter-toggle {
      display: inline-flex;
    }
    .filter-toggle.on {
      background: var(--accent-soft);
      border-color: transparent;
      color: var(--accent-soft-text);
    }
    .filters {
      display: none;
      padding: 0 14px 12px;
    }
    .filters.open {
      display: flex;
    }
    .filters select,
    .filters input:not([type='checkbox']) {
      min-height: 40px;
      font-size: 16px;
    }
    .exclude {
      flex: 1 1 100%;
    }
    .album {
      padding: 8px 14px;
      gap: 10px;
    }
    .fmt {
      width: 40px;
      height: 40px;
    }
    .details-body {
      padding: 14px;
    }
    .details-foot {
      padding-left: 14px;
      padding-right: 14px;
    }
    th.len,
    td.len {
      display: none;
    }
    .fsub {
      display: block;
    }
  }

  @media (max-width: 420px) {
    .hide-xs {
      display: none;
    }
  }
</style>

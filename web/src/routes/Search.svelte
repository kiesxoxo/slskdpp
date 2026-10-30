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
    return h.slot_free ? 'free slot' : `queue ${h.queue_length}`;
  }
</script>

<div class="page" class:has-details={selected}>
  <aside class="recent">
    <form class="search-form" onsubmit={start}>
      <input
        type="search"
        placeholder="Artist, album, track…"
        bind:value={query}
        aria-label="Search query"
        disabled={!session.connected}
      />
      <button class="primary" disabled={starting || !session.connected || !query.trim()}>Search</button>
    </form>
    {#if !session.connected}
      <p class="muted small">Not connected to the Soulseek server.</p>
    {/if}
    <h2>Recent</h2>
    <ul>
      {#each searches as s (s.id)}
        <li class:current={s.id === id}>
          <a href={href(['search', String(s.id)])} class="ellipsis" title={s.query}>{s.query}</a>
          <span class="muted small">{s.id === id ? hits.length : s.folders}</span>
          {#if s.id === id ? active : s.active}<span class="dot good pulse" title="Collecting results"></span>{/if}
          <button class="ghost x" title="Remove search" aria-label="Remove search {s.query}" onclick={() => remove(s)}>×</button>
        </li>
      {:else}
        <li class="muted small">No searches yet.</li>
      {/each}
    </ul>
  </aside>

  <section class="results">
    {#if error}<p class="error banner" role="alert">{error}</p>{/if}
    {#if id === null}
      <div class="hint muted">
        <p>Search the Soulseek network. Results stream in and are grouped by album, best sources first.</p>
      </div>
    {:else}
      <div class="toolbar">
        <div class="summary">
          <span class="wish">
            {#if wished}
              <a href={href(['wishlist'])}>On the wishlist</a>
            {:else}
              <button onclick={addToWishlist} title="Keep searching for this on a schedule">+ Wishlist</button>
            {/if}
          </span>
          {#if active}<span class="dot good pulse"></span>{/if}
          <strong>{plural(albums.length, 'album')}</strong>
          <span class="muted">
            from {plural(users, 'user')} · {plural(fileCount, 'file')}{active ? ' · searching…' : ''}
          </span>
        </div>
        <div class="filters">
          {#each formatChoices as f (f)}
            <button class="chip" class:on={formats.includes(f)} aria-pressed={formats.includes(f)} onclick={() => toggleFormat(f)}>{f}</button>
          {/each}
          <select bind:value={minBitrate} aria-label="Minimum bitrate">
            <option value={0}>any bitrate</option>
            <option value={192}>≥ 192 kbps</option>
            <option value={256}>≥ 256 kbps</option>
            <option value={320}>≥ 320 kbps</option>
          </select>
          <label class="check"><input type="checkbox" bind:checked={freeOnly} /> free slot</label>
          <label class="check">
            min files <input type="number" min="0" class="narrow" bind:value={minFiles} />
          </label>
          <input class="exclude" placeholder="exclude users (a, b)" bind:value={excludeText} aria-label="Exclude users" />
        </div>
      </div>
      <div class="list">
        <VirtualList items={albums} rowHeight={62}>
          {#snippet row(a: Album)}
            {@const best = a.sources[0]}
            <button class="album" class:selected={selected?.key === a.key} onclick={() => select(a)}>
              <span class="main">
                <span class="name ellipsis">{a.name}</span>
                <span class="meta muted ellipsis">
                  {plural(a.file_count, 'file')} · {best.formats.slice(0, 3).join('/')} · {quality(best)} · {size(a.total_size)}
                </span>
              </span>
              <span class="side">
                <span class="who ellipsis">
                  <span class="dot" class:good={best.slot_free} class:warn={!best.slot_free}></span>
                  {best.user}
                </span>
                <span class="muted small">
                  {slotLabel(best)} · {speed(best.upload_speed)}{a.sources.length > 1 ? ` · +${a.sources.length - 1}` : ''}
                </span>
              </span>
            </button>
          {/snippet}
          {#snippet empty()}
            <p class="hint muted">
              {active ? 'Waiting for results…' : hits.length ? 'No results match the filters.' : 'No results.'}
            </p>
          {/snippet}
        </VirtualList>
      </div>
    {/if}
  </section>

  {#if selected && source}
    <section class="details" aria-label="Album details">
      <div class="details-head">
        <h2 class="ellipsis" title={selected.name}>{selected.name}</h2>
        <button class="ghost" aria-label="Close details" onclick={() => (selectedHit = null)}>×</button>
      </div>
      <p class="muted small">{plural(selected.file_count, 'file')} · {size(selected.total_size)} · {plural(selected.sources.length, 'source')}</p>
      <ul class="sources">
        {#each selected.sources as s (hitKey(s))}
          <li>
            <button class="source" class:on={s === source} onclick={() => (selectedHit = hitKey(s))}>
              <span class="who ellipsis">
                <span class="dot" class:good={s.slot_free} class:warn={!s.slot_free}></span>
                {s.user}
              </span>
              <span class="muted small">{slotLabel(s)} · {speed(s.upload_speed)} · {quality(s)}</span>
            </button>
          </li>
        {/each}
      </ul>
      <div class="folder">
        <span class="mono ellipsis" title={source.folder}>{source.folder}</span>
        <a href={href(['browse', source.user], { folder: source.folder })}>Browse {source.user}</a>
      </div>
      {#key hitKey(source)}
        <div class="get">
          <DownloadButton
            username={source.user}
            folder={source.folder}
            files={source.files.map((f) => ({ path: f.path, size: f.size }))}
            label="Download folder"
          />
        </div>
      {/key}
      <div class="files">
        <table class="files">
          <thead>
            <tr><th>File</th><th class="num">Size</th><th class="num opt">Quality</th><th class="num">Length</th><th class="act"></th></tr>
          </thead>
          <tbody>
            {#each source.files as f (f.path)}
              <tr>
                <td class="ellipsis" title={f.name}>{f.name}</td>
                <td class="num">{size(f.size)}</td>
                <td class="num opt">{f.bit_depth && f.sample_rate ? `${f.bit_depth}/${f.sample_rate / 1000}k` : f.bitrate ? `${f.bitrate}k` : ''}</td>
                <td class="num">{duration(f.duration)}</td>
                <td class="act">
                  <DownloadButton compact username={source.user} files={[{ path: f.path, size: f.size }]} label="Download {f.name}" />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {/if}
</div>

<style>
  .page {
    display: grid;
    grid-template-columns: 260px minmax(0, 1fr);
    height: 100%;
  }
  .page.has-details {
    grid-template-columns: 260px minmax(0, 1fr) minmax(320px, 38%);
  }
  .small {
    font-size: 12.5px;
  }
  .recent {
    border-right: 1px solid var(--border);
    background: var(--surface);
    padding: 12px;
    overflow-y: auto;
  }
  .search-form {
    display: flex;
    gap: 6px;
  }
  .search-form input {
    flex: 1;
  }
  h2 {
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--muted);
    margin: 18px 0 6px;
  }
  .recent ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .recent li {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 2px 0 2px 8px;
    border-radius: var(--radius);
  }
  .recent li.current {
    background: var(--accent-soft);
  }
  .recent li a {
    flex: 1;
    color: var(--text);
  }
  .x {
    padding: 2px 8px;
    color: var(--muted);
  }
  .results {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }
  .banner {
    margin: 12px 16px 0;
  }
  .hint {
    padding: 24px 16px;
  }
  .toolbar {
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .summary {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .wish {
    order: 9;
    margin-left: auto;
    font-size: 13px;
  }
  .wish button {
    padding: 3px 10px;
    font-size: 13px;
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
  }
  .filters select,
  .filters input {
    padding: 4px 8px;
    font-size: 13px;
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
  .exclude {
    width: 170px;
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
    text-align: left;
    border: 0;
    border-bottom: 1px solid var(--border);
    border-radius: 0;
    background: transparent;
    padding: 8px 16px;
  }
  .album.selected {
    background: var(--accent-soft);
  }
  .main,
  .side {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .main {
    flex: 1;
  }
  .side {
    width: 34%;
    align-items: flex-end;
    text-align: right;
  }
  .name {
    font-weight: 600;
  }
  .meta {
    font-size: 12.5px;
  }
  .who {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .details {
    border-left: 1px solid var(--border);
    background: var(--surface);
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
    padding: 12px 16px;
  }
  .details-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .details-head h2 {
    flex: 1;
    margin: 0;
    font-size: 16px;
    text-transform: none;
    letter-spacing: 0;
    color: var(--text);
  }
  .details p {
    margin: 4px 0 10px;
  }
  .sources {
    list-style: none;
    margin: 0 0 10px;
    padding: 0;
    max-height: 30%;
    overflow-y: auto;
  }
  .source {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    width: 100%;
    border: 0;
    background: transparent;
    padding: 6px 8px;
  }
  .source.on {
    background: var(--accent-soft);
  }
  .folder {
    display: flex;
    gap: 10px;
    justify-content: space-between;
    align-items: baseline;
    padding: 8px 0;
    border-top: 1px solid var(--border);
  }
  .folder a {
    white-space: nowrap;
  }
  .get {
    padding: 0 0 10px;
  }
  th.act {
    width: 44px;
  }
  td.act {
    text-align: right;
  }
  .files {
    flex: 1;
    overflow-y: auto;
  }
  @media (max-width: 1000px) {
    .page,
    .page.has-details {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto minmax(0, 1fr);
    }
    .recent {
      border-right: 0;
      border-bottom: 1px solid var(--border);
      max-height: 170px;
    }
    .details {
      position: fixed;
      inset: 48px 0 0 0;
      z-index: 10;
      border-left: 0;
    }
    .side {
      width: 40%;
    }
  }
</style>

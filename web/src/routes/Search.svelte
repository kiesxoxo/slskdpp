<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { SvelteMap, SvelteSet } from 'svelte/reactivity';
  import { api, errorText, type FileInfo, type Hit, type SearchView } from '../lib/api';
  import {
    activeFilters,
    compile,
    groupAlbums,
    hitKey,
    rank,
    sortAlbums,
    sortDefaults,
    toSearchFilter,
    ungrouped,
    type Album,
    type Filter,
    type QualityFilter,
    type SortKey,
  } from '../lib/albums';
  import { subscribe } from '../lib/events';
  import { ago, duration, plural, size, speed } from '../lib/format';
  import { downloadOf, isActive } from '../lib/downloads.svelte';
  import { folderParts, isAudioExt, qualityLabel, summarize } from '../lib/quality';
  import { phone } from '../lib/media.svelte';
  import { queueFiles } from '../lib/queue';
  import { href, navigate } from '../lib/router.svelte';
  import { session } from '../lib/session.svelte';
  import { toast } from '../lib/toast.svelte';
  import VirtualList from '../lib/VirtualList.svelte';
  import DownloadButton from '../lib/DownloadButton.svelte';
  import FileRow from '../lib/FileRow.svelte';
  import QualityBadge from '../lib/QualityBadge.svelte';
  import Icon from '../lib/Icon.svelte';

  let { id }: { id: number | null } = $props();

  let query = $state('');
  let searches = $state<SearchView[]>([]);
  let view = $state<SearchView | null>(null);
  let error = $state('');
  let starting = $state(false);
  let active = $state(false);

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

  // How far the search's listening window has run.
  let now = $state(Date.now());
  $effect(() => {
    if (!active) return;
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });
  let windowLeft = $derived(view && active ? Math.max(0, Math.round((Date.parse(view.ends) - now) / 1000)) : 0);
  let windowPct = $derived.by(() => {
    if (!view || !active) return 100;
    const from = Date.parse(view.started);
    const span = Date.parse(view.ends) - from;
    return span > 0 ? Math.min(100, Math.max(2, ((now - from) / span) * 100)) : 100;
  });

  // Filters.
  let formats = $state<string[]>([]);
  let quality = $state<QualityFilter>('any');
  let freeOnly = $state(false);
  let minFiles = $state(0);
  let excludeText = $state('');
  let text = $state('');
  let showFilters = $state(false); // phones
  let moreOpen = $state(false);

  let filter: Filter = $derived({
    formats,
    quality,
    freeOnly,
    minFiles: minFiles || 0,
    exclude: excludeText
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean),
    text,
  });
  let nActive = $derived(activeFilters(filter));

  function toggleFormat(f: string) {
    formats = formats.includes(f) ? formats.filter((x) => x !== f) : [...formats, f];
  }

  function clearFilters() {
    formats = [];
    quality = 'any';
    freeOnly = false;
    minFiles = 0;
    excludeText = '';
    text = '';
  }

  function hideUser(user: string) {
    const list = filter.exclude;
    if (!list.includes(user)) excludeText = [...list, user].join(', ');
    toast({ message: `Hiding results from ${user}`, timeout: 3000 });
  }

  // View preferences, remembered per browser.
  const prefsKey = 'slskdpp-search-view';
  function loadPrefs(): { grouped?: boolean; expandAll?: boolean } {
    try {
      return JSON.parse(localStorage.getItem(prefsKey) ?? '{}');
    } catch {
      return {};
    }
  }
  const prefs = loadPrefs();
  let grouped = $state(prefs.grouped ?? true);
  let expandAll = $state(prefs.expandAll ?? false);
  $effect(() => {
    const v = JSON.stringify({ grouped, expandAll });
    try {
      localStorage.setItem(prefsKey, v);
    } catch {
      // storage may be blocked
    }
  });

  // Sorting.
  let sortKey = $state<SortKey>('relevance');
  let sortDir = $state<1 | -1>(1);
  const sortLabels: Record<SortKey, string> = {
    relevance: 'Best match',
    quality: 'Best quality',
    queue: 'Free slots first',
    speed: 'Fastest users',
    size: 'Largest',
    length: 'Longest',
    tracks: 'Most tracks',
    name: 'Name',
    user: 'User',
  };

  function sortBy(k: SortKey) {
    if (sortKey === k && k !== 'relevance') sortDir = sortDir === 1 ? -1 : 1;
    else {
      sortKey = k;
      sortDir = sortDefaults[k];
    }
  }

  // Results.
  let pass = $derived(compile(filter));
  let ranked = $derived(rank(hits.filter(pass)));
  let albums = $derived(sortAlbums(grouped ? groupAlbums(ranked) : ungrouped(ranked), sortKey, sortDir));
  let unfiltered = $derived(nActive === 0 ? albums.length : grouped ? groupAlbums(rank([...hits])).length : hits.length);
  let users = $derived(new Set(hits.map((h) => h.user)).size);
  let fileCount = $derived(hits.reduce((n, h) => n + h.file_count, 0));
  let unit = $derived(grouped ? 'album' : 'folder');
  // The most common audio formats in the results, offered as chips.
  let formatChoices = $derived.by(() => {
    const counts = new Map<string, number>();
    for (const h of hits) for (const f of h.formats) if (isAudioExt(f)) counts.set(f, (counts.get(f) ?? 0) + 1);
    const top = [...counts].sort((a, b) => b[1] - a[1]).slice(0, 6).map(([f]) => f);
    return [...new Set([...formats, ...top])];
  });

  // Expansion, the chosen source and picked files follow hits rather than
  // albums, so they survive albums regrouping as results stream in.
  const opened = new SvelteSet<string>();
  const closed = new SvelteSet<string>();
  const chosenKeys = new SvelteSet<string>();
  const allSources = new SvelteSet<string>();
  const picked = new SvelteMap<string, Set<string>>();
  const none = new Set<string>();

  function isOpen(a: Album): boolean {
    if (expandAll) return !a.sources.some((s) => closed.has(hitKey(s)));
    return a.sources.some((s) => opened.has(hitKey(s)));
  }

  function toggle(a: Album) {
    const open = isOpen(a);
    for (const s of a.sources) {
      opened.delete(hitKey(s));
      closed.delete(hitKey(s));
    }
    if (expandAll && open) closed.add(hitKey(a.sources[0]));
    else if (!expandAll && !open) opened.add(hitKey(a.sources[0]));
  }

  function setExpandAll(on: boolean) {
    expandAll = on;
    opened.clear();
    closed.clear();
  }

  function chosen(a: Album): Hit {
    return a.sources.find((s) => chosenKeys.has(hitKey(s))) ?? a.sources[0];
  }

  function choose(a: Album, s: Hit) {
    for (const x of a.sources) chosenKeys.delete(hitKey(x));
    chosenKeys.add(hitKey(s));
  }

  const showsAll = (a: Album) => a.sources.some((s) => allSources.has(hitKey(s)));
  const pickedOf = (s: Hit) => picked.get(hitKey(s)) ?? none;

  function pick(s: Hit, path: string) {
    const next = new Set(pickedOf(s));
    if (!next.delete(path)) next.add(path);
    picked.set(hitKey(s), next);
  }

  function pickAll(s: Hit, on: boolean) {
    picked.set(hitKey(s), on ? new Set(s.files.map((f) => f.path)) : new Set());
  }

  // Files in track order.
  const trackMemo = new WeakMap<FileInfo[], FileInfo[]>();
  function tracks(s: Hit): FileInfo[] {
    let t = trackMemo.get(s.files);
    if (!t) {
      t = [...s.files].sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }));
      trackMemo.set(s.files, t);
    }
    return t;
  }

  // What to download for a source: the picked files, or the folder.
  function request(s: Hit) {
    const sel = pickedOf(s);
    const files = (sel.size ? s.files.filter((f) => sel.has(f.path)) : s.files).map((f) => ({ path: f.path, size: f.size }));
    return { files, folder: sel.size ? '' : s.folder };
  }

  function downloadAlbum(a: Album) {
    const s = chosen(a);
    const r = request(s);
    queueFiles(s.user, r.files, r.folder);
  }

  // How much of a source is already in downloads.
  function inDownloads(s: Hit): { done: number; active: number } {
    let done = 0;
    let act = 0;
    for (const f of s.files) {
      const d = downloadOf(s.user, f.path);
      if (!d) continue;
      if (d.state === 'completed') done++;
      else if (isActive(d)) act++;
    }
    return { done, active: act };
  }

  // The list is flat: each album's row, then, when it is open, its
  // sources, folder, files and download bar, each of a fixed height.
  type Row =
    | { kind: 'album'; a: Album; n: number }
    | { kind: 'source'; a: Album; s: Hit; on: boolean; n: number }
    | { kind: 'more'; a: Album; hidden: number }
    | { kind: 'folder'; a: Album; s: Hit }
    | { kind: 'file'; a: Album; s: Hit; f: FileInfo }
    | { kind: 'actions'; a: Album; s: Hit };

  const maxSources = 5;

  let flat = $derived.by(() => {
    const rows: Row[] = [];
    const albumRow: number[] = [];
    albums.forEach((a, n) => {
      albumRow.push(rows.length);
      rows.push({ kind: 'album', a, n });
      if (!isOpen(a)) return;
      const s = chosen(a);
      if (a.sources.length > 1) {
        const all = showsAll(a);
        let list = all ? a.sources : a.sources.slice(0, maxSources);
        if (!list.includes(s)) list = [...list, s];
        for (const x of list) rows.push({ kind: 'source', a, s: x, on: x === s, n });
        if (!all && a.sources.length > list.length) rows.push({ kind: 'more', a, hidden: a.sources.length - list.length });
      }
      rows.push({ kind: 'folder', a, s });
      for (const f of tracks(s)) rows.push({ kind: 'file', a, s, f });
      rows.push({ kind: 'actions', a, s });
    });
    return { rows, albumRow };
  });

  let heights = $derived(
    phone.current
      ? { album: 92, source: 64, more: 44, folder: 48, file: 54, actions: 70 }
      : { album: 58, source: 42, more: 36, folder: 40, file: 34, actions: 60 },
  );
  const rowHeight = (r: Row) => heights[r.kind];

  function rowKey(r: Row): string {
    switch (r.kind) {
      case 'album':
        return 'a' + hitKey(r.a.sources[0]);
      case 'more':
        return 'm' + hitKey(r.a.sources[0]);
      case 'file':
        return 'f' + hitKey(r.s) + '\u0000' + r.f.path;
      default:
        return r.kind + hitKey(r.s);
    }
  }

  // Keyboard: / to search, ↑↓ or j/k between albums, → and ← to open and
  // close, d to download.
  let list = $state<{ scrollToIndex(i: number): void }>();
  let searchInput = $state<HTMLInputElement>();
  let activeN = $state(-1);

  function focusAlbum(n: number) {
    const i = flat.albumRow[n];
    if (i === undefined) return;
    activeN = n;
    list?.scrollToIndex(i);
    tick().then(() => document.querySelector<HTMLElement>(`[data-album="${n}"]`)?.focus());
  }

  function keydown(e: KeyboardEvent) {
    const t = e.target as HTMLElement;
    const typing = !!t.closest?.('input, textarea, select, [contenteditable]');
    if (e.key === '/' && !typing) {
      e.preventDefault();
      searchInput?.focus();
      searchInput?.select();
      return;
    }
    if (typing || e.metaKey || e.ctrlKey || e.altKey || !albums.length) return;
    if (t !== document.body && !t.closest?.('.results')) return;
    const cur = activeN < albums.length ? activeN : -1;
    const a = cur >= 0 ? albums[cur] : null;
    switch (e.key) {
      case 'ArrowDown':
      case 'j':
        e.preventDefault();
        focusAlbum(Math.min(cur + 1, albums.length - 1));
        break;
      case 'ArrowUp':
      case 'k':
        e.preventDefault();
        focusAlbum(Math.max(cur - 1, 0));
        break;
      case 'ArrowRight':
        if (a && !isOpen(a)) toggle(a);
        break;
      case 'ArrowLeft':
      case 'Escape':
        if (a && isOpen(a)) {
          toggle(a);
          focusAlbum(cur);
        }
        break;
      case 'd':
        if (a) downloadAlbum(a);
        break;
    }
  }

  // A click anywhere on an album row opens it, except on its links and
  // buttons, or when it ends a text selection.
  function rowClick(e: MouseEvent, a: Album) {
    if ((e.target as HTMLElement).closest('a, button, input, label')) return;
    if (window.getSelection()?.toString()) return;
    toggle(a);
  }

  function closeMore(e: MouseEvent) {
    if (moreOpen && !(e.target as HTMLElement).closest('.more-wrap')) moreOpen = false;
  }

  let wished = $state(false);
  async function addToWishlist() {
    if (!view) return;
    try {
      await api.addWishlist(view.query, toSearchFilter(filter), false);
      wished = true;
      toast({ kind: 'success', message: `“${view.query}” is on your wishlist`, action: { label: 'Open', href: href(['wishlist']) } });
    } catch (e) {
      toast({ kind: 'error', message: errorText(e) });
    }
  }

  const browseHref = (s: Hit) => href(['browse', s.user], { folder: s.folder });
  const trackCount = (s: Hit) => summarize(s.files).audio || s.file_count;
</script>

<svelte:window onkeydown={keydown} onclick={closeMore} />

{#snippet slot(h: Hit)}
  {#if h.slot_free}
    <span class="slot free" title="A download can start right away"><span class="dot good"></span>Free</span>
  {:else}
    <span class="slot busy" title="{plural(h.queue_length, 'download')} waiting in {h.user}'s queue">Queue {h.queue_length.toLocaleString()}</span>
  {/if}
{/snippet}

{#snippet th(key: SortKey, label: string, cls = '')}
  <span class="th {cls}" role="columnheader" aria-sort={sortKey === key ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}>
    <button class:sorted={sortKey === key} onclick={() => sortBy(key)} title="Sort by {label.toLowerCase()}">
      {label}
      {#if sortKey === key}<Icon name={sortDir === 1 ? 'sortUp' : 'sortDown'} size={12} stroke={2.2} />{/if}
    </button>
  </span>
{/snippet}

{#snippet tableHead()}
  <div class="grid head-row thead" role="row">
    <span></span>
    {@render th('name', grouped ? 'Album' : 'Folder', 'left')}
    {@render th('quality', 'Quality', 'left')}
    {@render th('tracks', 'Tracks')}
    {@render th('size', 'Size')}
    {@render th('length', 'Length', 'c-len')}
    {@render th('user', 'User', 'left user')}
    {@render th('speed', 'Speed', 'c-speed')}
    {@render th('queue', 'Slot', 'left')}
    <span></span>
  </div>
{/snippet}

{#snippet moreFields()}
  <label class="field-line">
    <span>Min tracks</span>
    <input type="number" min="0" inputmode="numeric" class="narrow" bind:value={minFiles} />
  </label>
  <label class="field-line">
    <span>Hide users</span>
    <input class="exclude" placeholder="alice, bob" bind:value={excludeText} aria-label="Hide users (comma-separated)" />
  </label>
{/snippet}

<div class="page">
  <div class="head">
    <form class="search-form" onsubmit={start}>
      <label class="field big">
        <Icon name="search" size={18} />
        <input
          bind:this={searchInput}
          type="search"
          enterkeyhint="search"
          placeholder={session.connected ? 'Artist, album or track' : 'Not connected to the Soulseek server'}
          bind:value={query}
          aria-label="Search query"
          disabled={!session.connected}
        />
        <kbd class="slash" aria-hidden="true">/</kbd>
      </label>
      <button class="primary" disabled={starting || !session.connected || !query.trim()}>Search</button>
    </form>
    {#if id !== null && searches.length}
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
    <div class="landing">
      {#if searches.length}
        <div class="section-title">Recent searches</div>
        <ul class="rows card recent-list">
          {#each searches as s (s.id)}
            <li>
              <a href={href(['search', String(s.id)])}>
                <span class="r-icon" class:live={s.active}><Icon name={s.active ? 'signal' : 'search'} size={17} /></span>
                <span class="r-main">
                  <span class="r-query ellipsis">{s.query}</span>
                  <span class="muted small">
                    {s.active ? 'Searching… · ' : ''}{plural(s.folders, 'folder')} from {plural(s.responses, 'user')} · {plural(s.files, 'file')}
                  </span>
                </span>
                <span class="muted small r-ago">{ago(s.started)}</span>
              </a>
              <button class="icon-btn small" title="Remove" aria-label="Remove search {s.query}" onclick={() => remove(s)}><Icon name="close" size={16} /></button>
            </li>
          {/each}
        </ul>
      {:else}
        <div class="empty">
          <span class="empty-icon"><Icon name="search" size={26} /></span>
          <strong>Find music on Soulseek</strong>
          <p>Results stream in as people answer, grouped into albums with the best sources first.</p>
        </div>
      {/if}
      <div class="tips muted small">
        <span><Icon name="info" size={15} /> Put <code>-word</code> in a search to leave out results that contain it.</span>
        <span class="hide-phone">Press <kbd>/</kbd> to search, <kbd>↑</kbd> <kbd>↓</kbd> to move through results, <kbd>→</kbd> to open one and <kbd>D</kbd> to download it.</span>
      </div>
    </div>
  {:else}
    <div class="toolbar">
      <div class="summary">
        {#if active}<span class="dot good pulse" title="Collecting results"></span>{/if}
        <strong class="num">
          {#if nActive && unfiltered !== albums.length}{albums.length.toLocaleString()} of {plural(unfiltered, unit)}{:else}{plural(albums.length, unit)}{/if}
        </strong>
        <span class="muted ellipsis num">· {plural(users, 'user')} · {plural(fileCount, 'file')}</span>
        {#if active && windowLeft > 0}
          <span class="window" title="Peers can answer for another {duration(windowLeft) || '0:00'}">
            <span class="bar"><span style:width="{windowPct}%"></span></span>
            <span class="muted small num">{duration(windowLeft) || '0:00'}</span>
          </span>
        {/if}
      </div>
      <div class="actions">
        <button class="small filter-toggle" class:on={showFilters} onclick={() => (showFilters = !showFilters)} aria-expanded={showFilters}>
          <Icon name="filter" size={16} /> Filters
          {#if nActive}<span class="count">{nActive}</span>{/if}
        </button>
        {#if wished}
          <a class="small-link" href={href(['wishlist'])}><Icon name="check" size={16} /> <span class="hide-xs">On wishlist</span></a>
        {:else}
          <button class="small" onclick={addToWishlist} title="Keep searching for this on a schedule">
            <Icon name="wishlist" size={16} /> <span class="hide-xs">Wishlist</span>
          </button>
        {/if}
      </div>
    </div>

    <div class="filters" class:open={showFilters}>
      <label class="field text-filter">
        <Icon name="filter" size={15} />
        <input type="search" placeholder="Filter results" bind:value={text} aria-label="Filter results; -word leaves out" />
      </label>
      {#if formatChoices.length}
        <div class="chips" role="group" aria-label="Formats">
          {#each formatChoices as f (f)}
            <button class="chip" class:on={formats.includes(f)} aria-pressed={formats.includes(f)} onclick={() => toggleFormat(f)}>{f.toUpperCase()}</button>
          {/each}
        </div>
      {/if}
      <select bind:value={quality} aria-label="Quality">
        <option value="any">Any quality</option>
        <option value="hires">Hi-res (24-bit)</option>
        <option value="lossless">Lossless</option>
        <option value="320">320 kbps or better</option>
        <option value="256">256 kbps or better</option>
        <option value="192">192 kbps or better</option>
      </select>
      <button class="chip" class:on={freeOnly} aria-pressed={freeOnly} onclick={() => (freeOnly = !freeOnly)}>
        <span class="dot" class:good={freeOnly}></span> Free slot
      </button>
      {#if phone.current}
        {@render moreFields()}
      {:else}
        <div class="more-wrap">
          <button class="chip" class:on={minFiles > 0 || filter.exclude.length > 0} aria-expanded={moreOpen} onclick={() => (moreOpen = !moreOpen)}>
            More <Icon name="down" size={14} stroke={2.2} />
          </button>
          {#if moreOpen}
            <div class="popover more-pop">{@render moreFields()}</div>
          {/if}
        </div>
      {/if}
      {#if nActive}<button class="ghost small" onclick={clearFilters}>Clear</button>{/if}
      <span class="spacer"></span>
      <select class="sort" value={sortKey} onchange={(e) => sortBy((e.currentTarget as HTMLSelectElement).value as SortKey)} aria-label="Sort by">
        {#each Object.entries(sortLabels) as [k, label] (k)}
          <option value={k}>{label}</option>
        {/each}
      </select>
      <label class="toggle" title="Show the same album from different users as one row">
        <input type="checkbox" checked={grouped} onchange={() => (grouped = !grouped)} />
        <span class="track"><span class="knob"></span></span>
        <span class="label">Group albums</span>
      </label>
      <label class="toggle" title="Show every result's files">
        <input type="checkbox" checked={expandAll} onchange={() => setExpandAll(!expandAll)} />
        <span class="track"><span class="knob"></span></span>
        <span class="label">Expand all</span>
      </label>
    </div>

    <div class="results">
      <VirtualList bind:this={list} items={flat.rows} {rowHeight} key={rowKey} header={albums.length > 0 && !phone.current ? tableHead : undefined}>
        {#snippet row(r: Row)}
          {#if r.kind === 'album'}
            {@const a = r.a}
            {@const s = chosen(a)}
            {@const p = folderParts(s.folder)}
            {@const sum = summarize(s.files)}
            {@const open = isOpen(a)}
            {@const dl = inDownloads(s)}
            <!-- The caret button is the keyboard control; the row click is a shortcut for the mouse. -->
            <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
            <div class="grid album" class:open onclick={(e) => rowClick(e, a)}>
              <button
                class="caret"
                data-album={r.n}
                aria-expanded={open}
                aria-label="{open ? 'Hide' : 'Show'} files of {p.name} from {s.user}"
                onclick={() => toggle(a)}
                onfocus={() => (activeN = r.n)}
              >
                <Icon name="chevron" size={15} stroke={2.2} />
              </button>
              <span class="title">
                <span class="t1">
                  <span class="name ellipsis" title={s.folder}>{p.name}</span>
                  {#if dl.active}
                    <span class="dl active" title="{dl.active} of {s.files.length} files downloading or queued"><Icon name="download" size={13} stroke={2.2} /></span>
                  {:else if dl.done}
                    <span class="dl done" title="{dl.done} of {s.files.length} files downloaded"><Icon name="check" size={13} stroke={2.4} /></span>
                  {/if}
                  <span class="phone-q"><QualityBadge files={s.files} /></span>
                </span>
                <span class="t2 ellipsis">
                  {p.parent || s.user}<span class="narrow-only">{sum.duration ? ' · ' + duration(sum.duration) : ''} · {speed(s.upload_speed)}</span>
                </span>
                <span class="t3 ellipsis num">
                  <span class={s.slot_free ? 'free-text' : 'busy-text'}>{s.slot_free ? 'Free' : `Queue ${s.queue_length}`}</span>
                  · {plural(trackCount(s), 'track')}{sum.duration ? ' · ' + duration(sum.duration) : ''} · {size(s.total_size)} · {speed(s.upload_speed)} · {s.user}{a.sources.length > 1
                    ? ` +${a.sources.length - 1}`
                    : ''}
                </span>
              </span>
              <span class="cell"><QualityBadge files={s.files} /></span>
              <span class="cell num r" title="{sum.audio} audio files, {s.file_count} in all">{trackCount(s)}</span>
              <span class="cell num r">{size(s.total_size)}</span>
              <span class="cell num r c-len">{duration(sum.duration) || '—'}</span>
              <span class="cell user">
                <a class="ellipsis" href={browseHref(s)} title="Browse {s.user}">{s.user}</a>
                {#if a.sources.length > 1}
                  <span class="plus" title={a.sources.slice(0, 12).map((x) => x.user).join(', ') + (a.sources.length > 12 ? ', …' : '')}>+{a.sources.length - 1}</span>
                {/if}
              </span>
              <span class="cell num r c-speed">{speed(s.upload_speed)}</span>
              <span class="cell">{@render slot(s)}</span>
              <span class="cell act">
                <DownloadButton compact username={s.user} folder={s.folder} files={s.files.map((f) => ({ path: f.path, size: f.size }))} label="Download {p.name} from {s.user}" />
              </span>
            </div>
          {:else if r.kind === 'source'}
            {@const s = r.s}
            {@const p = folderParts(s.folder)}
            {@const sum = summarize(s.files)}
            <label class="sub grid source" class:on={r.on} title={s.folder}>
              <input type="radio" name="src-{r.n}" checked={r.on} onchange={() => choose(r.a, s)} aria-label="Download from {s.user}" />
              <span class="src-path ellipsis"><span class="narrow-only">{`${speed(s.upload_speed)} · `}</span>{p.name}{p.parent ? ` — ${p.parent}` : ''}</span>
              <span class="cell"><QualityBadge files={s.files} /></span>
              <span class="cell num r">{trackCount(s)}</span>
              <span class="cell num r">{size(s.total_size)}</span>
              <span class="cell num r c-len">{duration(sum.duration) || '—'}</span>
              <span class="cell src-user ellipsis">{s.user}</span>
              <span class="cell num r c-speed">{speed(s.upload_speed)}</span>
              <span class="cell src-slot">{@render slot(s)}</span>
              <span class="cell act">
                <button class="icon-btn small" title="Hide results from {s.user}" aria-label="Hide results from {s.user}" onclick={() => hideUser(s.user)}>
                  <Icon name="hide" size={16} />
                </button>
              </span>
              <span class="src-phone ellipsis num">{qualityLabel(sum)} · {plural(trackCount(s), 'track')} · {size(s.total_size)} · {speed(s.upload_speed)}</span>
            </label>
          {:else if r.kind === 'more'}
            <div class="sub more-row">
              <button class="ghost small" onclick={() => allSources.add(hitKey(r.a.sources[0]))}>
                <Icon name="down" size={14} stroke={2.2} /> Show {plural(r.hidden, 'more source')}
              </button>
            </div>
          {:else if r.kind === 'folder'}
            {@const s = r.s}
            {@const sel = pickedOf(s)}
            <div class="sub grid folder-row">
              <input
                type="checkbox"
                checked={sel.size > 0 && sel.size === s.files.length}
                {@attach (el: HTMLInputElement) => {
                  el.indeterminate = sel.size > 0 && sel.size < s.files.length;
                }}
                onchange={() => pickAll(s, sel.size < s.files.length)}
                aria-label="Select all files"
              />
              <span class="path ellipsis" title={s.folder}><Icon name="folder" size={15} /><span class="mono ellipsis">{s.folder}</span></span>
              <a href={browseHref(s)} class="browse-link">Browse {s.user}'s folder</a>
            </div>
          {:else if r.kind === 'file'}
            <div class="sub file-wrap">
              <FileRow file={r.f} username={r.s.user} indent={phone.current ? 14 : 44} checked={pickedOf(r.s).has(r.f.path)} ontoggle={() => pick(r.s, r.f.path)} />
            </div>
          {:else}
            {@const s = r.s}
            {@const sel = pickedOf(s)}
            {@const req = request(s)}
            {@const bytes = req.files.reduce((n, f) => n + f.size, 0)}
            <div class="sub actions-row">
              {#key hitKey(s) + ':' + sel.size}
                <DownloadButton
                  username={s.user}
                  folder={req.folder}
                  files={req.files}
                  label={sel.size ? `Download ${plural(sel.size, 'file')}` : 'Download folder'}
                  caption={sel.size ? size(bytes) : `${plural(s.files.length, 'file')} · ${size(bytes)}`}
                />
              {/key}
              {#if sel.size}
                <button class="ghost small" onclick={() => pickAll(s, false)}>Clear selection</button>
              {:else}
                <span class="muted small hint">or tick files to download only those</span>
              {/if}
            </div>
          {/if}
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
</div>

<style>
  .page {
    display: flex;
    flex-direction: column;
    height: 100%;
    background: var(--surface);
  }
  .head {
    padding: 18px 24px 12px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .search-form {
    display: flex;
    gap: 8px;
  }
  .field.big input {
    min-height: 44px;
    font-size: 15px;
    border-radius: 11px;
    padding-right: 40px;
    background: var(--bg);
  }
  .field.big input:focus {
    background: var(--surface);
  }
  .slash {
    position: absolute;
    right: 12px;
    pointer-events: none;
  }
  .field.big input:focus ~ .slash,
  .field.big input:disabled ~ .slash {
    display: none;
  }
  .search-form button {
    min-height: 44px;
    padding: 0 22px;
    border-radius: 11px;
  }
  .recent {
    display: flex;
    gap: 6px;
    overflow-x: auto;
    scrollbar-width: none;
    margin: 0 -24px;
    padding: 0 24px;
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
    margin: 0 24px 8px;
  }

  /* Landing. */
  .landing {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 12px 24px 40px;
    display: flex;
    flex-direction: column;
    gap: 10px;
    background: var(--bg);
    border-top: 1px solid var(--border);
  }
  .landing > * {
    width: 100%;
    max-width: 860px;
    margin-left: auto;
    margin-right: auto;
  }
  .landing .section-title {
    margin-top: 12px;
  }
  .recent-list > li {
    display: flex;
    align-items: center;
    gap: 6px;
    padding-right: 8px;
  }
  .recent-list a {
    display: flex;
    align-items: center;
    gap: 12px;
    flex: 1;
    min-width: 0;
    padding: 12px 6px 12px 14px;
    color: var(--text);
  }
  .recent-list a:hover {
    text-decoration: none;
  }
  .recent-list li:hover {
    background: var(--row-hover);
  }
  .recent-list li:first-child {
    border-radius: var(--radius-lg) var(--radius-lg) 0 0;
  }
  .recent-list li:last-child {
    border-radius: 0 0 var(--radius-lg) var(--radius-lg);
  }
  .r-icon {
    display: grid;
    place-items: center;
    flex: none;
    width: 36px;
    height: 36px;
    border-radius: 10px;
    background: var(--surface-2);
    color: var(--muted);
  }
  .r-icon.live {
    background: var(--good-soft);
    color: var(--good);
  }
  .r-main {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
  }
  .r-query {
    font-weight: 600;
  }
  .r-ago {
    white-space: nowrap;
  }
  .tips {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px 2px;
  }
  .tips span {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  code {
    font-family: var(--mono);
    font-size: 12px;
    padding: 1px 5px;
    border-radius: 5px;
    background: var(--surface-3);
    color: var(--text);
  }

  /* Toolbar and filters. */
  .toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 2px 24px 10px;
  }
  .summary {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .summary strong {
    white-space: nowrap;
  }
  .window {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    margin-left: 8px;
    flex: none;
  }
  .window .bar {
    width: 72px;
    height: 4px;
  }
  .window .bar > span {
    background: var(--good);
    transition: width 1s linear;
  }
  .actions {
    display: flex;
    gap: 6px;
    flex: none;
  }
  .filter-toggle {
    display: none;
  }
  .count {
    min-width: 18px;
    padding: 0 5px;
    line-height: 18px;
    border-radius: 999px;
    background: var(--accent);
    color: var(--accent-text);
    font-size: 11px;
    font-weight: 700;
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
    padding: 0 24px 12px;
    border-bottom: 1px solid var(--border);
  }
  .text-filter {
    flex: 0 1 220px;
  }
  .filters input:not([type='checkbox']):not([type='radio']),
  .filters select {
    min-height: 30px;
    padding: 4px 10px;
    font-size: 13px;
  }
  .text-filter input {
    padding-left: 32px !important;
  }
  .text-filter > :global(.icon) {
    left: 10px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .filters .chip {
    min-height: 30px;
  }
  .spacer {
    flex: 1;
  }
  .more-wrap {
    position: relative;
  }
  .more-pop {
    top: calc(100% + 6px);
    left: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .field-line {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    font-size: 13px;
    color: var(--muted);
  }
  .narrow {
    width: 72px;
  }
  .exclude {
    width: 160px;
  }

  /* Results table. */
  .results {
    flex: 1;
    min-height: 0;
    --pad: 24px;
    --cols: 26px minmax(0, 1fr) 118px 56px 72px 62px minmax(110px, 170px) 84px 92px 40px;
  }
  .grid {
    display: grid;
    grid-template-columns: var(--cols);
    align-items: center;
    gap: 0 10px;
    padding: 0 calc(var(--pad) - 8px) 0 calc(var(--pad) - 6px);
  }
  .head-row {
    height: 34px;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
  }
  .th {
    display: flex;
    justify-content: flex-end;
    min-width: 0;
  }
  .th.left {
    justify-content: flex-start;
  }
  .th button {
    min-height: 0;
    height: 26px;
    padding: 0 4px;
    margin: 0 -4px;
    border: 0;
    background: transparent;
    font: inherit;
    letter-spacing: inherit;
    text-transform: inherit;
    color: inherit;
    gap: 3px;
  }
  .th button:hover:not(:disabled) {
    background: transparent;
    color: var(--text);
  }
  .th button.sorted {
    color: var(--accent);
  }

  .album {
    position: relative;
    height: 100%;
    border-bottom: 1px solid var(--border);
  }
  .album:hover {
    background: var(--row-hover);
  }
  .album.open {
    background: var(--surface-2);
    border-bottom-color: transparent;
  }
  .album {
    cursor: pointer;
  }
  .album:has(.caret:focus-visible) {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }
  .caret {
    width: 26px;
    height: 26px;
    min-height: 0;
    padding: 0;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: var(--faint);
  }
  .caret:focus-visible {
    outline: none;
  }
  .caret :global(.icon) {
    transition: transform 0.15s;
  }
  .album.open .caret {
    color: var(--accent);
  }
  .album.open .caret :global(.icon) {
    transform: rotate(90deg);
  }
  .title {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .t1 {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .name {
    font-weight: 600;
  }
  .t2 {
    font-size: 12.5px;
    color: var(--muted);
  }
  .t3,
  .phone-q {
    display: none;
  }
  .narrow-only {
    display: none;
  }
  .dl {
    display: grid;
    place-items: center;
    flex: none;
    width: 18px;
    height: 18px;
    border-radius: 50%;
  }
  .dl.done {
    background: var(--good-soft);
    color: var(--good);
  }
  .dl.active {
    background: var(--accent-soft);
    color: var(--accent-soft-text);
  }
  .cell {
    min-width: 0;
    font-size: 13px;
  }
  .cell.r {
    text-align: right;
  }
  .cell.user,
  .src-user,
  .th.user {
    padding-left: 10px;
  }
  .cell.user {
    display: flex;
    align-items: center;
    gap: 5px;
  }
  .cell.user a {
    color: var(--text);
    font-weight: 500;
  }
  .plus {
    flex: none;
    padding: 0 6px;
    border-radius: 999px;
    background: var(--surface-3);
    color: var(--muted);
    font-size: 11px;
    font-weight: 600;
    line-height: 18px;
  }
  .slot {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    font-weight: 600;
    white-space: nowrap;
  }
  .slot.free {
    color: var(--good);
  }
  .slot.busy {
    color: var(--warn);
  }
  .free-text {
    color: var(--good);
    font-weight: 600;
  }
  .busy-text {
    color: var(--warn);
    font-weight: 600;
  }
  .act {
    display: flex;
    justify-content: flex-end;
  }

  /* Rows inside an open album. */
  .sub {
    height: 100%;
    background: var(--surface-2);
  }
  .source {
    font-size: 13px;
    cursor: pointer;
  }
  .source input {
    justify-self: center;
  }
  .source:hover {
    background: color-mix(in srgb, var(--surface-3) 50%, var(--surface-2));
  }
  .src-path {
    font-size: 12.5px;
    color: var(--muted);
  }
  .src-user {
    font-weight: 500;
  }
  .source.on .src-user {
    font-weight: 650;
    color: var(--accent-soft-text);
  }
  .source .act button {
    color: var(--faint);
  }
  .src-phone {
    display: none;
  }
  .more-row {
    display: flex;
    align-items: center;
    padding-left: calc(var(--pad) + 22px);
  }
  .more-row button {
    color: var(--accent);
  }
  .folder-row {
    border-top: 1px dashed var(--border);
    color: var(--faint);
  }
  .folder-row input {
    justify-self: center;
  }
  .path {
    grid-column: 2 / -3;
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .path .mono {
    color: var(--muted);
  }
  .browse-link {
    grid-column: -3 / -1;
    justify-self: end;
    font-size: 12.5px;
    white-space: nowrap;
  }
  .file-wrap {
    --row-hover: color-mix(in srgb, var(--surface-3) 50%, var(--surface-2));
  }
  .file-wrap :global(.file) {
    height: 100%;
  }
  /* Wide screens: files line up with the album columns. */
  @media (min-width: 1101px) {
    .file-wrap :global(.file) {
      grid-template-columns: var(--cols);
      gap: 0 10px;
      padding: 0 calc(var(--pad) - 8px) 0 calc(var(--pad) - 6px);
    }
    .file-wrap :global(.file > *) {
      grid-row: 1;
    }
    .file-wrap :global(.file > :first-child) {
      grid-column: 1;
      justify-self: center;
    }
    .file-wrap :global(.file .name) {
      grid-column: 2;
    }
    .file-wrap :global(.file .q) {
      grid-column: 3;
      text-align: left;
      padding-left: 8px;
    }
    .file-wrap :global(.file .sz) {
      grid-column: 5;
    }
    .file-wrap :global(.file .len) {
      grid-column: 6;
    }
    .file-wrap :global(.file .act) {
      grid-column: 10;
    }
  }
  .actions-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 0 var(--pad) 4px calc(var(--pad) + 30px);
    border-bottom: 1px solid var(--border);
  }
  .hint {
    white-space: nowrap;
  }

  @media (max-width: 1100px) {
    .results {
      --cols: 26px minmax(0, 1fr) 108px 50px 68px minmax(90px, 130px) 84px 40px;
    }
    .c-len,
    .c-speed {
      display: none !important;
    }
    .narrow-only {
      display: inline;
    }
  }

  @media (max-width: 860px) {
    .hint {
      display: none;
    }
  }

  /* Phones. */
  @media (max-width: 720px) {
    .head {
      padding: 12px 14px 10px;
    }
    .slash {
      display: none;
    }
    .recent {
      margin: 0 -14px;
      padding: 0 14px;
    }
    .toolbar {
      padding: 2px 14px 10px;
    }
    .window .bar {
      display: none;
    }
    .err {
      margin: 0 14px 8px;
    }
    .landing {
      padding: 8px 14px 32px;
    }
    .hide-phone {
      display: none !important;
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
      padding: 0 14px 14px;
      gap: 10px;
    }
    .filters.open {
      display: flex;
    }
    .text-filter {
      flex: 1 1 100%;
    }
    .filters input:not([type='checkbox']):not([type='radio']),
    .filters select {
      min-height: 40px;
      font-size: 16px;
    }
    .filters .chip {
      min-height: 36px;
    }
    .field-line {
      flex: 1 1 100%;
    }
    .exclude {
      flex: 1;
      width: auto;
    }
    .spacer {
      display: none;
    }
    .sort {
      flex: 1 1 100%;
    }
    .results {
      --pad: 14px;
    }
    .album {
      display: flex;
      align-items: center;
      padding: 0 14px;
    }
    .album > .caret {
      position: absolute;
      inset: 0;
      width: 100%;
      height: 100%;
      opacity: 0;
    }
    .album > .cell {
      display: none;
    }
    .title {
      flex: 1;
      gap: 2px;
    }
    .t1 .name {
      flex: 1;
      font-size: 15px;
    }
    .phone-q {
      display: inline-flex;
      flex: none;
    }
    .t2 {
      font-size: 13px;
    }
    .t3 {
      display: block;
      font-size: 13px;
      color: var(--muted);
    }
    .narrow-only {
      display: none;
    }
    .album.open {
      box-shadow: inset 3px 0 0 var(--accent);
    }
    .source {
      grid-template-columns: 24px minmax(0, 1fr) auto;
      grid-template-areas:
        'radio user slot'
        'radio meta meta';
      align-content: center;
      gap: 2px 10px;
      padding: 0 var(--pad);
    }
    .source input {
      grid-area: radio;
    }
    .source > .cell,
    .src-path {
      display: none;
    }
    .source > .src-user {
      display: block;
      grid-area: user;
      font-size: 14px;
    }
    .source > .src-slot {
      display: block;
      grid-area: slot;
    }
    .src-phone {
      display: block;
      grid-area: meta;
      font-size: 13px;
      color: var(--muted);
    }
    .more-row {
      padding-left: calc(var(--pad) - 6px);
    }
    .folder-row {
      display: flex;
      gap: 10px;
      padding: 0 var(--pad);
    }
    .browse-link {
      display: none;
    }
    .actions-row {
      padding: 0 var(--pad) 4px;
    }
    .actions-row :global(.main) {
      flex: 1;
    }
  }

  @media (max-width: 420px) {
    .hide-xs {
      display: none;
    }
  }
</style>

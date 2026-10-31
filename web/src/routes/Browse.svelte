<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { SvelteSet } from 'svelte/reactivity';
  import { api, errorText, type BrowseResult, type Dir } from '../lib/api';
  import { duration, plural, size } from '../lib/format';
  import { navigate } from '../lib/router.svelte';
  import { summarize } from '../lib/quality';
  import VirtualList from '../lib/VirtualList.svelte';
  import DownloadButton from '../lib/DownloadButton.svelte';
  import FileRow from '../lib/FileRow.svelte';
  import QualityBadge from '../lib/QualityBadge.svelte';
  import Icon from '../lib/Icon.svelte';

  let { user, folder }: { user: string; folder: string } = $props();

  interface Node {
    path: string;
    name: string;
    depth: number;
    children: Node[];
    dir?: Dir;
    total: number; // files in this folder and below
  }

  // The page is remounted per user, so the prop only seeds the input.
  let input = $state(untrack(() => user));
  let data = $state.raw<BrowseResult | null>(null);
  let loading = $state(false);
  let error = $state('');
  let filterText = $state('');
  const expanded = new SvelteSet<string>();
  let selectedPath = $state<string | null>(null);

  // Remote paths are backslash separated; some clients use slashes.
  const split = (p: string) => p.split(/[\\/]/).filter(Boolean);

  let tree = $derived.by(() => {
    const roots: Node[] = [];
    const byPath = new Map<string, Node>();
    const dirs = [...(data?.dirs ?? []), ...(data?.private_dirs ?? [])];
    for (const d of dirs) {
      const parts = split(d.name);
      let siblings = roots;
      let path = '';
      parts.forEach((name, depth) => {
        path = path ? path + '\\' + name : name;
        let node = byPath.get(path);
        if (!node) {
          node = { path, name, depth, children: [], total: 0 };
          byPath.set(path, node);
          siblings.push(node);
        }
        siblings = node.children;
      });
      const node = byPath.get(path);
      if (node) node.dir = d;
    }
    const total = (n: Node): number => {
      n.children.sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }));
      n.total = (n.dir?.files.length ?? 0) + n.children.reduce((sum, c) => sum + total(c), 0);
      return n.total;
    };
    roots.forEach(total);
    return { roots, byPath };
  });

  let stats = $derived.by(() => {
    let files = 0;
    let bytes = 0;
    for (const d of data?.dirs ?? []) {
      files += d.files.length;
      for (const f of d.files) bytes += f.size;
    }
    return { folders: data?.dirs?.length ?? 0, files, bytes };
  });

  // The rows on screen: the expanded tree, or a flat list of folders
  // matching the filter.
  let rows = $derived.by(() => {
    const needle = filterText.trim().toLowerCase();
    if (needle) {
      return [...tree.byPath.values()].filter((n) => n.dir && n.path.toLowerCase().includes(needle));
    }
    const out: Node[] = [];
    const walk = (nodes: Node[]) => {
      for (const n of nodes) {
        out.push(n);
        if (expanded.has(n.path)) walk(n.children);
      }
    };
    walk(tree.roots);
    return out;
  });

  let selected = $derived(selectedPath ? tree.byPath.get(selectedPath) : undefined);
  let selectedBytes = $derived(selected?.dir?.files.reduce((n, f) => n + f.size, 0) ?? 0);
  let files = $derived(
    selected?.dir ? [...selected.dir.files].sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true })) : [],
  );
  let sum = $derived(selected?.dir ? summarize(selected.dir.files) : null);
  let crumbs = $derived(selected ? split(selected.path) : []);

  // Files picked for download in the open folder.
  const picked = new SvelteSet<string>();
  $effect(() => {
    selectedPath; // a new folder starts with nothing picked
    picked.clear();
  });
  function pick(path: string) {
    if (!picked.delete(path)) picked.add(path);
  }
  let pickedFiles = $derived(files.filter((f) => picked.has(f.path)));
  let pickedBytes = $derived(pickedFiles.reduce((n, f) => n + f.size, 0));

  function revealInitial() {
    // Open single-child chains from the roots (the usual "@@share\Music").
    let level = tree.roots;
    while (level.length === 1) {
      expanded.add(level[0].path);
      level = level[0].children;
    }
    if (folder) {
      const want = split(folder).join('\\');
      const parts = split(want);
      for (let i = 1; i < parts.length; i++) expanded.add(parts.slice(0, i).join('\\'));
      if (tree.byPath.has(want)) selectedPath = want;
    }
  }

  async function load() {
    loading = true;
    error = '';
    try {
      data = await api.browse(user);
      revealInitial();
    } catch (e) {
      error = errorText(e);
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    if (user) load();
  });

  function go(e: SubmitEvent) {
    e.preventDefault();
    const u = input.trim();
    if (!u) return;
    if (u === user) load();
    else navigate(['browse', u]);
  }

  function toggle(n: Node) {
    if (n.children.length && !filterText) {
      if (expanded.has(n.path)) expanded.delete(n.path);
      else expanded.add(n.path);
    }
    if (n.dir) selectedPath = n.path;
  }

  // reveal opens a folder in the tree, and shows its files if it has any.
  function reveal(path: string) {
    const parts = split(path);
    for (let i = 1; i <= parts.length; i++) expanded.add(parts.slice(0, i).join('\\'));
    selectedPath = tree.byPath.get(path)?.dir ? path : null;
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Escape') selectedPath = null;
  }
</script>

<svelte:window onkeydown={keydown} />

<div class="page" class:has-files={selected?.dir}>
  <section class="tree">
    <div class="head">
      <form class="user-form" onsubmit={go}>
        <label class="field">
          <Icon name="user" size={18} />
          <input placeholder="Username" bind:value={input} aria-label="Username to browse" autocapitalize="off" autocomplete="off" />
        </label>
        <button class="primary" disabled={loading || !input.trim()}>Browse</button>
      </form>
      {#if data}
        <label class="field">
          <Icon name="filter" size={16} />
          <input type="search" class="filter" placeholder="Filter folders" bind:value={filterText} aria-label="Filter folders" />
        </label>
        <p class="muted small">
          {plural(stats.folders, 'folder')} · {plural(stats.files, 'file')} · {size(stats.bytes)}
          {#if data.private_dirs?.length}· {plural(data.private_dirs.length, 'locked folder')}{/if}
        </p>
      {/if}
    </div>
    <div class="list">
      {#if loading}
        <div class="empty">
          <span class="empty-icon pulse"><Icon name="browse" size={26} /></span>
          <strong>Asking {user} for their shares…</strong>
          <p>Large shares can take a minute.</p>
        </div>
      {:else if error}
        <div class="empty">
          <span class="empty-icon err"><Icon name="close" size={26} /></span>
          <strong>Could not browse {user}</strong>
          <p class="error">{error}</p>
        </div>
      {:else if !user}
        <div class="empty">
          <span class="empty-icon"><Icon name="browse" size={26} /></span>
          <strong>Browse someone's shares</strong>
          <p>Enter a username, or open one from a search result.</p>
        </div>
      {:else}
        <VirtualList items={rows} rowHeight={40}>
          {#snippet row(n: Node)}
            <button
              class="node"
              class:selected={n.path === selectedPath}
              style:padding-left="{(filterText ? 0 : n.depth) * 18 + 12}px"
              onclick={() => toggle(n)}
              title={n.path}
            >
              <span class="caret" class:open={expanded.has(n.path)} class:none={!n.children.length || !!filterText}>
                <Icon name="chevron" size={14} stroke={2.2} />
              </span>
              <Icon name="browse" size={17} />
              <span class="ellipsis">{filterText ? n.path : n.name}</span>
              <span class="count">{n.total}</span>
            </button>
          {/snippet}
          {#snippet empty()}
            <div class="empty"><strong>{filterText ? 'No folders match' : 'Nothing shared'}</strong></div>
          {/snippet}
        </VirtualList>
      {/if}
    </div>
  </section>

  <section class="files-pane">
    {#if selected?.dir}
      <div class="files-head">
        <button class="icon-btn back" aria-label="Back to folders" onclick={() => (selectedPath = null)}><Icon name="back" /></button>
        <div class="title">
          <nav class="crumbs small" aria-label="Folder path">
            {#each crumbs.slice(0, -1) as c, i (i)}
              <button class="crumb" onclick={() => reveal(crumbs.slice(0, i + 1).join('\\'))}>{c}</button>
              <Icon name="chevron" size={12} stroke={2.2} />
            {/each}
          </nav>
          <h2 class="ellipsis" title={selected.path}>{selected.name}</h2>
          <p class="muted small meta">
            {#if sum?.format}<QualityBadge files={selected.dir.files} />{/if}
            <span class="num">{plural(selected.dir.files.length, 'file')} · {size(selectedBytes)}{sum?.duration ? ' · ' + duration(sum.duration) : ''}</span>
          </p>
        </div>
      </div>
      <div class="scroll">
        {#if files.length}
          <div class="file-head thead">
            <input
              type="checkbox"
              checked={picked.size > 0 && picked.size === files.length}
              {@attach (el: HTMLInputElement) => {
                el.indeterminate = picked.size > 0 && picked.size < files.length;
              }}
              onchange={() => {
                const all = picked.size < files.length;
                picked.clear();
                if (all) for (const f of files) picked.add(f.path);
              }}
              aria-label="Select all files"
            />
            <span>File</span>
            <span class="r">Quality</span>
            <span class="r">Length</span>
            <span class="r">Size</span>
            <span></span>
          </div>
          {#each files as f (f.path)}
            <FileRow file={f} username={user} indent={16} checked={picked.has(f.path)} ontoggle={() => pick(f.path)} />
          {/each}
        {:else}
          <p class="muted none">No files directly in this folder.</p>
        {/if}
      </div>
      {#key selected.path + ':' + picked.size}
        <div class="files-foot">
          {#if picked.size}
            <DownloadButton
              username={user}
              files={pickedFiles.map((f) => ({ path: f.path, size: f.size }))}
              label="Download {plural(picked.size, 'file')}"
              caption={size(pickedBytes)}
            />
            <button class="ghost small" onclick={() => picked.clear()}>Clear selection</button>
          {:else}
            <DownloadButton
              username={user}
              folder={selected.dir.name}
              files={selected.dir.files.map((f) => ({ path: f.path, size: f.size }))}
              label="Download folder"
              caption="{plural(selected.dir.files.length, 'file')} · {size(selectedBytes)}"
            />
          {/if}
        </div>
      {/key}
    {:else if data}
      <div class="empty">
        <span class="empty-icon"><Icon name="music" size={26} /></span>
        <strong>Pick a folder</strong>
        <p>Its files show here, ready to download.</p>
      </div>
    {/if}
  </section>
</div>

<style>
  .page {
    display: grid;
    grid-template-columns: minmax(300px, 40%) minmax(0, 1fr);
    grid-template-rows: minmax(0, 1fr);
    height: 100%;
  }
  .tree {
    display: flex;
    flex-direction: column;
    min-height: 0;
    border-right: 1px solid var(--border);
    background: var(--surface);
  }
  .head {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 18px 16px 12px;
    border-bottom: 1px solid var(--border);
  }
  .user-form {
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
  .list {
    flex: 1;
    min-height: 0;
  }
  .node {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    height: 100%;
    min-height: 0;
    border: 0;
    border-radius: 0;
    background: transparent;
    text-align: left;
    padding-right: 14px;
    font-weight: 400;
    color: var(--text);
  }
  .node :global(.icon) {
    color: var(--faint);
  }
  .node.selected {
    background: var(--accent-soft);
    color: var(--accent-soft-text);
  }
  .node.selected :global(.icon) {
    color: var(--accent-soft-text);
  }
  .caret {
    display: grid;
    place-items: center;
    width: 14px;
    transition: transform 0.15s;
  }
  .caret.open {
    transform: rotate(90deg);
  }
  .caret.none {
    visibility: hidden;
  }
  .node .ellipsis {
    flex: 1;
  }
  .count {
    font-size: 12px;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .empty-icon.err {
    background: var(--bad-soft);
    color: var(--bad);
  }
  .files-pane {
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
    background: var(--surface);
  }
  .files-head {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 16px 20px 14px;
    border-bottom: 1px solid var(--border);
  }
  .title {
    display: flex;
    flex-direction: column;
    gap: 3px;
    flex: 1;
    min-width: 0;
  }
  .title h2 {
    font-size: 18px;
    letter-spacing: -0.01em;
  }
  .crumbs {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 2px;
    color: var(--faint);
  }
  .crumbs:empty {
    display: none;
  }
  .crumb {
    min-height: 0;
    padding: 0 3px;
    border: 0;
    background: transparent;
    color: var(--muted);
    font-size: 12.5px;
    font-weight: 500;
  }
  .crumb:hover:not(:disabled) {
    background: transparent;
    color: var(--accent);
  }
  .meta {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 2px;
  }
  .back {
    display: none;
  }
  .scroll {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 0 0 8px;
  }
  .file-head {
    position: sticky;
    top: 0;
    z-index: 1;
    display: grid;
    grid-template-columns: 28px minmax(0, 1fr) 72px 56px 72px 36px;
    align-items: center;
    gap: 0 8px;
    height: 36px;
    padding: 0 16px;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
  }
  .file-head .r {
    text-align: right;
  }
  .none {
    padding: 20px;
  }
  .files-foot {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 20px;
    border-top: 1px solid var(--border);
  }
  .files-foot :global(.main) {
    flex: 1;
    max-width: 420px;
  }

  @media (max-width: 860px) {
    .page {
      grid-template-columns: minmax(0, 1fr);
    }
    .tree {
      border-right: 0;
    }
    .files-pane {
      display: none;
    }
    .has-files .files-pane {
      display: flex;
      position: fixed;
      inset: 0;
      z-index: 30;
      animation: rise 0.2s ease;
    }
    .files-head {
      padding: calc(8px + env(safe-area-inset-top)) 14px 10px 6px;
    }
    .back {
      display: inline-flex;
    }
    .files-foot {
      padding: 12px 14px calc(12px + env(safe-area-inset-bottom));
    }
    .files-foot :global(.main) {
      max-width: none;
    }
  }

  @media (max-width: 720px) {
    .head {
      padding: 12px 14px 10px;
    }
    .node {
      font-size: 15px;
    }
    .file-head {
      display: none;
    }
  }
</style>

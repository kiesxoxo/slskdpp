<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { SvelteSet } from 'svelte/reactivity';
  import { api, errorText, type BrowseResult, type Dir } from '../lib/api';
  import { duration, plural, size } from '../lib/format';
  import { navigate } from '../lib/router.svelte';
  import VirtualList from '../lib/VirtualList.svelte';
  import DownloadButton from '../lib/DownloadButton.svelte';

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
</script>

<div class="page">
  <section class="tree">
    <form class="bar" onsubmit={go}>
      <input placeholder="Username" bind:value={input} aria-label="Username to browse" />
      <button class="primary" disabled={loading || !input.trim()}>Browse</button>
    </form>
    {#if data}
      <div class="bar">
        <input type="search" placeholder="Filter folders" bind:value={filterText} aria-label="Filter folders" />
      </div>
      <p class="muted small summary">
        {plural(stats.folders, 'folder')} · {plural(stats.files, 'file')} · {size(stats.bytes)}
        {#if data.private_dirs?.length}· {plural(data.private_dirs.length, 'locked folder')}{/if}
      </p>
    {/if}
    <div class="list">
      {#if loading}
        <p class="hint muted pulse">Asking {user} for their shares… large shares can take a minute.</p>
      {:else if error}
        <p class="hint error" role="alert">{error}</p>
      {:else if !user}
        <p class="hint muted">Enter a username to see what they share.</p>
      {:else}
        <VirtualList items={rows} rowHeight={30}>
          {#snippet row(n: Node)}
            <button
              class="node"
              class:selected={n.path === selectedPath}
              style:padding-left="{(filterText ? 0 : n.depth) * 16 + 10}px"
              onclick={() => toggle(n)}
              title={n.path}
            >
              <span class="caret">{n.children.length && !filterText ? (expanded.has(n.path) ? '▾' : '▸') : ''}</span>
              <span class="ellipsis">{filterText ? n.path : n.name}</span>
              <span class="count muted">{n.total}</span>
            </button>
          {/snippet}
          {#snippet empty()}
            <p class="hint muted">{filterText ? 'No folders match.' : 'Nothing shared.'}</p>
          {/snippet}
        </VirtualList>
      {/if}
    </div>
  </section>

  <section class="files-pane">
    {#if selected?.dir}
      <div class="head">
        <h2 class="mono ellipsis" title={selected.path}>{selected.path}</h2>
        <p class="muted small">{plural(selected.dir.files.length, 'file')} · {size(selectedBytes)}</p>
        {#key selected.path}
          <div class="get">
            <DownloadButton
              username={user}
              folder={selected.dir.name}
              files={selected.dir.files.map((f) => ({ path: f.path, size: f.size }))}
              label="Download folder"
            />
          </div>
        {/key}
      </div>
      <div class="scroll">
        <table class="files">
          <thead>
            <tr><th>File</th><th class="num">Size</th><th class="num opt">Quality</th><th class="num">Length</th><th class="act"></th></tr>
          </thead>
          <tbody>
            {#each selected.dir.files as f (f.path)}
              <tr>
                <td class="ellipsis" title={f.name}>{f.name}</td>
                <td class="num">{size(f.size)}</td>
                <td class="num opt">{f.bit_depth && f.sample_rate ? `${f.bit_depth}/${f.sample_rate / 1000}k` : f.bitrate ? `${f.bitrate}k` : ''}</td>
                <td class="num">{duration(f.duration)}</td>
                <td class="act">
                  <DownloadButton compact username={user} files={[{ path: f.path, size: f.size }]} label="Download {f.name}" />
                </td>
              </tr>
            {:else}
              <tr><td colspan="5" class="muted">No files directly in this folder.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else if data}
      <p class="hint muted">Pick a folder to see its files.</p>
    {/if}
  </section>
</div>

<style>
  .page {
    display: grid;
    grid-template-columns: minmax(280px, 40%) minmax(0, 1fr);
    height: 100%;
  }
  .small {
    font-size: 12.5px;
  }
  .tree {
    display: flex;
    flex-direction: column;
    min-height: 0;
    border-right: 1px solid var(--border);
    background: var(--surface);
  }
  .bar {
    display: flex;
    gap: 6px;
    padding: 12px 12px 0;
  }
  .bar input {
    flex: 1;
  }
  .summary {
    margin: 8px 12px;
  }
  .list {
    flex: 1;
    min-height: 0;
    margin-top: 6px;
    border-top: 1px solid var(--border);
  }
  .hint {
    padding: 16px;
    margin: 0;
  }
  .node {
    display: flex;
    align-items: center;
    gap: 4px;
    width: 100%;
    height: 100%;
    border: 0;
    border-radius: 0;
    background: transparent;
    text-align: left;
    padding-right: 10px;
  }
  .node.selected {
    background: var(--accent-soft);
  }
  .caret {
    width: 14px;
    flex: none;
    color: var(--muted);
  }
  .node .ellipsis {
    flex: 1;
  }
  .count {
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }
  .files-pane {
    background: var(--surface);
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
  }
  .head {
    padding: 12px 16px 4px;
  }
  .head h2 {
    margin: 0;
    font-weight: 600;
  }
  .head p {
    margin: 4px 0 0;
  }
  .get {
    margin-top: 10px;
  }
  th.act {
    width: 44px;
  }
  td.act {
    text-align: right;
  }
  .scroll {
    flex: 1;
    overflow-y: auto;
    padding: 0 8px;
  }
  @media (max-width: 800px) {
    .page {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: minmax(0, 55%) minmax(0, 45%);
    }
    .tree {
      border-right: 0;
      border-bottom: 1px solid var(--border);
    }
  }
</style>

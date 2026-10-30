<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorText, type LocalFolders, type ShareRoot, type ShareStats } from '../lib/api';
  import { subscribe } from '../lib/events';
  import { plural, size } from '../lib/format';

  let stats = $state<ShareStats | null>(null);
  let roots = $state<ShareRoot[]>([]);
  let error = $state('');
  let path = $state('');
  let alias = $state('');
  let adding = $state(false);
  let picker = $state<LocalFolders | null>(null);

  async function load() {
    try {
      const r = await api.shares();
      stats = r.stats;
      roots = r.roots;
    } catch (e) {
      error = errorText(e);
    }
  }

  onMount(() => {
    load();
    // Counts change as scans finish; poll while one runs.
    const stop = subscribe(0, { shares: () => load() });
    const poll = setInterval(() => stats?.scanning && load(), 2000);
    return () => {
      stop();
      clearInterval(poll);
    };
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
    adding = true;
    await run(async () => {
      await api.addShare(path.trim(), alias.trim());
      path = '';
      alias = '';
      picker = null;
    });
    adding = false;
  }

  async function browse(dir: string) {
    try {
      picker = await api.localFolders(dir);
      path = picker.path;
    } catch (e) {
      error = errorText(e);
    }
  }

  const join = (dir: string, name: string) => (dir.endsWith('/') ? dir + name : dir + '/' + name);
</script>

<div class="page">
  <div class="toolbar">
    <div class="summary">
      {#if stats?.scanning}<span class="dot good pulse"></span>{/if}
      <strong>{stats ? plural(stats.files, 'file') : '…'}</strong>
      <span class="muted">
        {#if stats}in {plural(stats.folders, 'folder')} · {size(stats.bytes)}{stats.scanning ? ' · scanning…' : ''}{/if}
      </span>
    </div>
    <button onclick={() => run(() => api.rescanShares())} disabled={stats?.scanning}>Rescan</button>
  </div>
  {#if error}<p class="error banner" role="alert">{error}</p>{/if}

  <div class="content">
    <ul class="roots">
      {#each roots as r (r.alias)}
        <li>
          <div class="info">
            <span class="alias">{r.alias}</span>
            <span class="mono muted ellipsis" title={r.path}>{r.path}</span>
            <span class="muted small">{plural(r.files, 'file')} in {plural(r.folders, 'folder')} · {size(r.bytes)}</span>
          </div>
          {#if r.from_config}
            <span class="chip" title="Set in the config file">config</span>
          {:else}
            <button onclick={() => run(() => api.removeShare(r.alias))}>Stop sharing</button>
          {/if}
        </li>
      {:else}
        <li class="muted">You are not sharing anything yet. Sharing helps the network and lets others answer your searches faster.</li>
      {/each}
    </ul>

    <form class="add" onsubmit={add}>
      <h2>Share a folder</h2>
      <div class="fields">
        <input placeholder="/path/to/music" bind:value={path} aria-label="Folder path" class="mono" />
        <button type="button" onclick={() => browse(path || '')}>Browse…</button>
      </div>
      {#if picker}
        <div class="picker">
          <div class="picker-head">
            <span class="mono ellipsis">{picker.path}</span>
            {#if picker.parent}<button type="button" class="ghost" onclick={() => browse(picker!.parent)}>Up</button>{/if}
          </div>
          <ul>
            {#each picker.folders as f (f)}
              <li><button type="button" class="ghost" onclick={() => browse(join(picker!.path, f))}>📁 {f}</button></li>
            {:else}
              <li class="muted small">No subfolders.</li>
            {/each}
          </ul>
        </div>
      {/if}
      <div class="fields">
        <input placeholder="Name others see (optional)" bind:value={alias} aria-label="Share name" />
        <button class="primary" disabled={adding || !path.trim()}>Share</button>
      </div>
      <p class="muted small">Files appear to others as <span class="mono">{alias.trim() || 'folder name'}\…</span>. Hidden files are never shared.</p>
    </form>
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
  .content {
    flex: 1;
    overflow-y: auto;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 20px;
    max-width: 820px;
  }
  .roots {
    list-style: none;
    margin: 0;
    padding: 0;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 10px;
  }
  .roots > li {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 14px;
  }
  .roots > li + li {
    border-top: 1px solid var(--border);
  }
  .info {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
  }
  .alias {
    font-weight: 600;
  }
  .add {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  h2 {
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--muted);
    margin: 0;
  }
  .fields {
    display: flex;
    gap: 6px;
  }
  .fields input {
    flex: 1;
  }
  .picker {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 10px;
    max-height: 260px;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .picker-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border);
  }
  .picker ul {
    list-style: none;
    margin: 0;
    padding: 4px;
    overflow-y: auto;
  }
  .picker li button {
    width: 100%;
    text-align: left;
    padding: 4px 8px;
  }
  .picker li.muted {
    padding: 6px 8px;
  }
  p {
    margin: 0;
  }
</style>

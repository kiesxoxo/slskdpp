<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorText, type LocalFolders, type ShareRoot, type ShareStats } from '../lib/api';
  import { subscribe } from '../lib/events';
  import { plural, size } from '../lib/format';
  import Icon from '../lib/Icon.svelte';
  import PageHead from '../lib/PageHead.svelte';

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

<div class="page-scroll">
  <div class="page-body">
    <PageHead title="Shares">
      {#snippet summary()}
        {#if stats?.scanning}<span class="dot good pulse"></span>{/if}
        <span>
          {#if stats}<strong class="strong">{plural(stats.files, 'file')}</strong> in {plural(stats.folders, 'folder')} · {size(stats.bytes)}{stats.scanning ? ' · scanning…' : ''}{/if}
        </span>
      {/snippet}
      {#snippet actions()}
        <button class="small" onclick={() => run(() => api.rescanShares())} disabled={stats?.scanning}><Icon name="retry" size={16} /> Rescan</button>
      {/snippet}
    </PageHead>
    {#if error}<p class="banner" role="alert">{error}</p>{/if}

    {#if roots.length}
      <ul class="rows card roots">
        {#each roots as r (r.alias)}
          <li>
            <span class="root-icon"><Icon name="browse" size={20} /></span>
            <div class="info">
              <span class="alias">{r.alias}</span>
              <span class="mono muted ellipsis" title={r.path}>{r.path}</span>
              <span class="muted small">{plural(r.files, 'file')} · {plural(r.folders, 'folder')} · {size(r.bytes)}</span>
            </div>
            {#if r.from_config}
              <span class="badge" title="Set in the config file">Config</span>
            {:else}
              <button class="small" onclick={() => run(() => api.removeShare(r.alias))}>Stop sharing</button>
            {/if}
          </li>
        {/each}
      </ul>
    {:else}
      <div class="card">
        <div class="empty">
          <span class="empty-icon"><Icon name="shares" size={26} /></span>
          <strong>You are not sharing anything yet</strong>
          <p>Sharing keeps Soulseek going, and many users only upload to people who share.</p>
        </div>
      </div>
    {/if}

    <form class="add card" onsubmit={add}>
      <h2 class="section-title">Share a folder</h2>
      <div class="row">
        <input class="mono path" placeholder="/path/to/music" bind:value={path} aria-label="Folder path" autocapitalize="off" />
        <button type="button" onclick={() => browse(path || '')}><Icon name="browse" size={17} /> Browse</button>
      </div>
      {#if picker}
        <div class="picker">
          <div class="picker-head">
            {#if picker.parent}
              <button type="button" class="icon-btn small" aria-label="Up one folder" onclick={() => browse(picker!.parent)}><Icon name="back" size={17} /></button>
            {/if}
            <span class="mono ellipsis">{picker.path}</span>
          </div>
          <ul>
            {#each picker.folders as f (f)}
              <li>
                <button type="button" class="ghost" onclick={() => browse(join(picker!.path, f))}>
                  <Icon name="browse" size={16} /> <span class="ellipsis">{f}</span>
                </button>
              </li>
            {:else}
              <li class="muted small none">No subfolders.</li>
            {/each}
          </ul>
        </div>
      {/if}
      <div class="row">
        <input placeholder="Name others see (optional)" bind:value={alias} aria-label="Share name" />
        <button class="primary" disabled={adding || !path.trim()}><Icon name="plus" size={18} /> Share</button>
      </div>
      <p class="muted small">Others see files as <span class="mono">{alias.trim() || 'folder name'}\…</span>. Hidden files are never shared.</p>
    </form>
  </div>
</div>

<style>
  .strong {
    color: var(--text);
  }
  .roots {
    overflow: hidden;
  }
  .roots > li {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 14px 16px;
  }
  .root-icon {
    display: grid;
    place-items: center;
    width: 40px;
    height: 40px;
    flex: none;
    border-radius: 11px;
    background: var(--accent-soft);
    color: var(--accent-soft-text);
  }
  .info {
    display: flex;
    flex-direction: column;
    gap: 1px;
    flex: 1;
    min-width: 0;
  }
  .alias {
    font-weight: 600;
    font-size: 15px;
  }
  .add {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 16px;
  }
  .row {
    display: flex;
    gap: 8px;
  }
  .row input {
    flex: 1;
  }
  .picker {
    display: flex;
    flex-direction: column;
    max-height: 280px;
    overflow: hidden;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
  }
  .picker-head {
    display: flex;
    align-items: center;
    gap: 6px;
    min-height: 40px;
    padding: 4px 10px 4px 4px;
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
    justify-content: flex-start;
    gap: 8px;
    min-height: 36px;
    font-weight: 400;
    color: var(--text);
  }
  .picker li button :global(.icon) {
    color: var(--faint);
  }
  .none {
    padding: 8px 10px;
  }
  @media (max-width: 720px) {
    .roots > li {
      flex-wrap: wrap;
    }
    .roots > li > button {
      margin-left: 54px;
    }
    .row {
      flex-wrap: wrap;
    }
    .row input {
      flex-basis: 100%;
    }
    .row button {
      flex: 1;
    }
  }
</style>

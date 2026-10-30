<script lang="ts">
  import { SvelteSet } from 'svelte/reactivity';
  import { api, errorText, type Download, type DownloadState } from '../lib/api';
  import { downloads, isActive, stateLabel } from '../lib/downloads.svelte';
  import { plural, size, speed } from '../lib/format';

  interface Group {
    key: string;
    folder: string;
    users: string[];
    items: Download[];
    bytes: number;
    total: number;
    active: number;
    failed: number;
    done: number;
    speed: number;
    newest: number;
  }

  let error = $state('');
  // Finished groups start collapsed; this remembers the user's choice.
  const toggled = new SvelteSet<string>();

  let groups = $derived.by(() => {
    const map = new Map<string, Group>();
    for (const d of downloads.values()) {
      const key = d.folder;
      let g = map.get(key);
      if (!g) {
        g = { key, folder: d.folder, users: [], items: [], bytes: 0, total: 0, active: 0, failed: 0, done: 0, speed: 0, newest: 0 };
        map.set(key, g);
      }
      g.items.push(d);
      if (!g.users.includes(d.username)) g.users.push(d.username);
      g.bytes += d.bytes;
      g.total += d.size;
      g.speed += d.speed;
      g.newest = Math.max(g.newest, d.id);
      if (isActive(d)) g.active++;
      else if (d.state === 'completed') g.done++;
      else g.failed++;
    }
    for (const g of map.values()) g.items.sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }));
    // Groups with work left first, newest first within each kind.
    return [...map.values()].sort((a, b) => Number(b.active > 0) - Number(a.active > 0) || b.newest - a.newest);
  });

  let totals = $derived.by(() => {
    let active = 0, done = 0, failed = 0, rate = 0;
    for (const d of downloads.values()) {
      if (isActive(d)) active++;
      else if (d.state === 'completed') done++;
      else failed++;
      rate += d.speed;
    }
    return { active, done, failed, rate };
  });

  function open(g: Group): boolean {
    const byDefault = g.active > 0 || g.failed > 0;
    return toggled.has(g.key) ? !byDefault : byDefault;
  }

  function toggle(g: Group) {
    if (toggled.has(g.key)) toggled.delete(g.key);
    else toggled.add(g.key);
  }

  async function run(action: () => Promise<unknown>) {
    error = '';
    try {
      await action();
    } catch (e) {
      error = errorText(e);
    }
  }

  const each = (items: Download[], pick: (d: Download) => boolean, act: (id: number) => Promise<void>) =>
    run(() => Promise.all(items.filter(pick).map((d) => act(d.id))));

  const clear = (states: DownloadState[]) => run(() => api.clearDownloads(states));

  function pct(bytes: number, total: number): number {
    return total > 0 ? Math.min(100, (bytes / total) * 100) : 0;
  }

  function detail(d: Download): string {
    switch (d.state) {
      case 'transferring':
        return `${size(d.bytes)} of ${size(d.size)} · ${speed(d.speed)}`;
      case 'queued_remote':
        return d.queue_place ? `place ${d.queue_place} in ${d.username}'s queue` : `waiting in ${d.username}'s queue`;
      case 'queued':
        if (d.searching) return `${d.error ? d.error + ' · ' : ''}looking for another user with this file`;
        if (d.error) {
          const wait = d.next_attempt ? Math.max(0, Math.round((Date.parse(d.next_attempt) - Date.now()) / 1000)) : 0;
          return wait > 0 ? `${d.error} · retrying in ${wait < 90 ? wait + 's' : Math.round(wait / 60) + ' min'}` : d.error;
        }
        return d.bytes > 0 ? `${size(d.bytes)} of ${size(d.size)} kept for resuming` : size(d.size);
      case 'completed':
        return size(d.size);
      default:
        return d.error || size(d.size);
    }
  }
</script>

<div class="page">
  <div class="toolbar">
    <div class="summary">
      {#if totals.active}<span class="dot good pulse"></span>{/if}
      <strong>{plural(totals.active, 'active download')}</strong>
      <span class="muted">
        {totals.rate > 0 ? speed(totals.rate) + ' · ' : ''}{totals.done} done{totals.failed ? ` · ${totals.failed} failed or cancelled` : ''}
      </span>
    </div>
    <div class="actions">
      <button disabled={!totals.done} onclick={() => clear(['completed'])}>Clear completed</button>
      <button disabled={!totals.failed} onclick={() => clear(['failed', 'cancelled'])}>Clear failed</button>
    </div>
  </div>
  {#if error}<p class="error banner" role="alert">{error}</p>{/if}

  <div class="list">
    {#each groups as g (g.key)}
      {@const isOpen = open(g)}
      <section class="group">
        <div class="group-head">
          <button class="ghost caret" aria-expanded={isOpen} aria-label="Show files in {g.folder}" onclick={() => toggle(g)}>
            {isOpen ? '▾' : '▸'}
          </button>
          <div class="group-title">
            <span class="name ellipsis" title={g.folder}>{g.folder}</span>
            <span class="muted small ellipsis">
              {g.users.join(', ')} · {g.done}/{g.items.length} files · {size(g.bytes)} of {size(g.total)}
              {g.speed > 0 ? ' · ' + speed(g.speed) : ''}
            </span>
          </div>
          <div class="group-actions">
            {#if g.failed}
              <button onclick={() => each(g.items, (d) => d.state === 'failed' || d.state === 'cancelled', api.retryDownload)}>Retry failed</button>
            {/if}
            {#if g.active}
              <button onclick={() => each(g.items, isActive, api.cancelDownload)}>Cancel</button>
            {:else}
              <button onclick={() => each(g.items, () => true, api.removeDownload)}>Remove</button>
            {/if}
          </div>
        </div>
        <div class="bar" class:done={!g.active && !g.failed}>
          <span style:width="{pct(g.bytes, g.total)}%"></span>
        </div>
        {#if isOpen}
          <ul>
            {#each g.items as d (d.id)}
              <li>
                <div class="file">
                  <span class="ellipsis" title={d.filename}>{d.name}</span>
                  <span class="muted small ellipsis" class:error={d.state === 'failed'}>{detail(d)}</span>
                  {#if d.state === 'transferring' || (d.bytes > 0 && isActive(d))}
                    <div class="bar thin"><span style:width="{pct(d.bytes, d.size)}%"></span></div>
                  {/if}
                </div>
                <span class="badge {d.searching ? 'searching' : d.state}">
                  {d.searching ? 'Finding source' : stateLabel[d.state]}{d.sources > 1 ? ` · source ${d.sources}` : ''}
                </span>
                <span class="file-actions">
                  {#if isActive(d)}
                    {#if d.state === 'queued'}
                      <button class="ghost" title="Try now" aria-label="Try {d.name} now" onclick={() => run(() => api.retryDownload(d.id))}>↻</button>
                    {/if}
                    <button class="ghost" title="Cancel" aria-label="Cancel {d.name}" onclick={() => run(() => api.cancelDownload(d.id))}>✕</button>
                  {:else}
                    {#if d.state !== 'completed'}
                      <button class="ghost" title="Retry" aria-label="Retry {d.name}" onclick={() => run(() => api.retryDownload(d.id))}>↻</button>
                    {/if}
                    <button class="ghost" title="Remove from list" aria-label="Remove {d.name}" onclick={() => run(() => api.removeDownload(d.id))}>🗑</button>
                  {/if}
                </span>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {:else}
      <p class="hint muted">Nothing downloading. Find an album in Search and press “Download folder”.</p>
    {/each}
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
  .actions {
    display: flex;
    gap: 6px;
  }
  .banner {
    margin: 10px 16px 0;
  }
  .list {
    flex: 1;
    overflow-y: auto;
    padding: 12px 16px 24px;
  }
  .hint {
    padding: 12px 0;
  }
  .group {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 10px;
    margin-bottom: 10px;
    overflow: hidden;
  }
  .group-head {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px 8px 6px;
  }
  .caret {
    padding: 2px 8px;
    color: var(--muted);
  }
  .group-title {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
  }
  .name {
    font-weight: 600;
  }
  .group-actions {
    display: flex;
    gap: 6px;
  }
  .group-actions button {
    padding: 4px 10px;
    font-size: 13px;
  }
  .bar {
    height: 3px;
    background: var(--surface-2);
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.4s ease;
  }
  .bar.done span {
    background: var(--good);
  }
  .bar.thin {
    height: 2px;
    margin-top: 4px;
    border-radius: 2px;
    overflow: hidden;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 7px 12px 7px 40px;
    border-top: 1px solid var(--border);
  }
  .file {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
  }
  .badge {
    font-size: 12px;
    padding: 2px 8px;
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--muted);
    white-space: nowrap;
  }
  .badge.transferring,
  .badge.initializing {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .badge.searching {
    color: var(--warn);
  }
  .badge.completed {
    color: var(--good);
  }
  .badge.failed {
    color: var(--bad);
  }
  .file-actions {
    display: flex;
    width: 64px;
    justify-content: flex-end;
  }
  .file-actions button {
    padding: 2px 8px;
    color: var(--muted);
  }
  @media (max-width: 600px) {
    .group-head {
      flex-wrap: wrap;
    }
    .group-actions {
      width: 100%;
      justify-content: flex-end;
    }
    li {
      padding-left: 12px;
      flex-wrap: wrap;
    }
    .file {
      flex-basis: 100%;
    }
    .badge {
      margin-left: auto;
    }
  }
</style>

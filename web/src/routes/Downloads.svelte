<script lang="ts">
  import { SvelteSet } from 'svelte/reactivity';
  import { api, errorText, type Download, type DownloadState } from '../lib/api';
  import { downloads, eta, isActive, stateLabel } from '../lib/downloads.svelte';
  import { left, size, speed } from '../lib/format';
  import { href } from '../lib/router.svelte';
  import Icon, { type IconName } from '../lib/Icon.svelte';
  import PageHead from '../lib/PageHead.svelte';

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

  type Show = 'all' | 'active' | 'done' | 'failed';
  let show = $state<Show>('all');
  const kindOf = (d: Download): Show => (isActive(d) ? 'active' : d.state === 'completed' ? 'done' : 'failed');

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

  // The groups and files the status filter lets through.
  let shown = $derived(
    groups
      .map((g) => ({ g, items: show === 'all' ? g.items : g.items.filter((d) => kindOf(d) === show) }))
      .filter((x) => x.items.length > 0),
  );

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

  function badgeIcon(d: Download): IconName {
    if (d.searching) return 'search';
    switch (d.state) {
      case 'transferring':
      case 'initializing':
        return 'download';
      case 'completed':
        return 'check';
      case 'failed':
        return 'alert';
      case 'cancelled':
        return 'close';
      case 'queued_remote':
        return 'queue';
    }
    return 'clock';
  }

  function groupEta(g: Group): string {
    return g.speed > 0 ? left(eta(g.total - g.bytes, g.speed)) : '';
  }

  function badgeClass(d: Download): string {
    if (d.searching) return 'warn';
    switch (d.state) {
      case 'transferring':
      case 'initializing':
        return 'accent';
      case 'completed':
        return 'good';
      case 'failed':
        return 'bad';
    }
    return '';
  }

  function detail(d: Download): string {
    switch (d.state) {
      case 'transferring':
        return [`${size(d.bytes)} of ${size(d.size)}`, speed(d.speed), left(eta(d.size - d.bytes, d.speed))].filter(Boolean).join(' · ');
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

<div class="page-scroll">
  <div class="page-body">
    <PageHead title="Downloads">
      {#snippet summary()}
        {#if totals.active}<span class="dot good pulse"></span>{/if}
        <span>
          <strong class="strong">{totals.active} active</strong>
          {totals.rate > 0 ? ' · ' + speed(totals.rate) : ''} · {totals.done} done{totals.failed ? ` · ${totals.failed} failed` : ''}
        </span>
      {/snippet}
      {#snippet actions()}
        <button class="small" disabled={!totals.done} onclick={() => clear(['completed'])}><Icon name="check" size={16} /> Clear done</button>
        <button class="small" disabled={!totals.failed} onclick={() => clear(['failed', 'cancelled'])}><Icon name="trash" size={16} /> Clear failed</button>
      {/snippet}
    </PageHead>
    {#if error}<p class="banner" role="alert">{error}</p>{/if}

    {#if downloads.size}
      <div class="seg" role="group" aria-label="Show">
        {#each [['all', 'All', downloads.size], ['active', 'Active', totals.active], ['done', 'Done', totals.done], ['failed', 'Failed', totals.failed]] as [k, label, n] (k)}
          <button class:on={show === k} aria-pressed={show === k} onclick={() => (show = k as Show)}>{label} <span class="n">{n}</span></button>
        {/each}
      </div>
    {/if}

    {#each shown as { g, items } (g.key)}
      {@const isOpen = open(g) || show !== 'all'}
      <section class="group card">
        <div class="group-head">
          <button class="icon-btn small caret" class:open={isOpen} aria-expanded={isOpen} aria-label="Show files in {g.folder}" onclick={() => toggle(g)}>
            <Icon name="chevron" size={16} stroke={2.2} />
          </button>
          <div class="group-title">
            <button class="name ellipsis" title={g.folder} onclick={() => toggle(g)}>{g.folder}</button>
            <span class="muted small ellipsis meta-line">
              {#each g.users as u, i (u)}{i ? ', ' : ''}<a href={href(['browse', u])}>{u}</a>{/each}
              · {g.done}/{g.items.length} files · {size(g.bytes)} of {size(g.total)}{g.speed > 0 ? ' · ' + speed(g.speed) : ''}{groupEta(g)
                ? ' · ' + groupEta(g)
                : ''}{g.failed ? ` · ${g.failed} failed` : ''}
            </span>
          </div>
          <span class="pct num" class:done={!g.active && !g.failed}>{Math.floor(pct(g.bytes, g.total))}%</span>
          <div class="group-actions">
            {#if g.failed}
              <button class="small" onclick={() => each(g.items, (d) => d.state === 'failed' || d.state === 'cancelled', api.retryDownload)}>
                <Icon name="retry" size={16} /> <span class="hide-xs">Retry</span>
              </button>
            {/if}
            {#if g.active}
              <button class="small" onclick={() => each(g.items, isActive, api.cancelDownload)}>Cancel</button>
            {:else}
              <button class="icon-btn small" title="Remove from list" aria-label="Remove {g.folder}" onclick={() => each(g.items, () => true, api.removeDownload)}>
                <Icon name="trash" size={17} />
              </button>
            {/if}
          </div>
        </div>
        <div class="bar group-bar" class:done={!g.active && !g.failed} class:failed={!g.active && g.failed > 0}>
          <span style:width="{pct(g.bytes, g.total)}%"></span>
        </div>
        {#if isOpen}
          <ul class="rows">
            {#each items as d (d.id)}
              <li>
                <div class="file">
                  <span class="ellipsis fname" title={d.filename}>{d.name}</span>
                  <span class="meta">
                    <span class="badge phone-only {badgeClass(d)}"><Icon name={badgeIcon(d)} size={12} stroke={2.2} />{d.searching ? 'Finding source' : stateLabel[d.state]}</span>
                    <span class="muted small ellipsis" class:error={d.state === 'failed'}>{detail(d)}</span>
                  </span>
                  {#if d.state === 'transferring' || (d.bytes > 0 && isActive(d))}
                    <div class="bar thin"><span style:width="{pct(d.bytes, d.size)}%"></span></div>
                  {/if}
                </div>
                <span class="badge wide-only {badgeClass(d)}" title={d.sources > 1 ? `Trying source ${d.sources}` : undefined}>
                  <Icon name={badgeIcon(d)} size={12} stroke={2.2} />
                  {d.searching ? 'Finding source' : stateLabel[d.state]}{d.sources > 1 ? ` · #${d.sources}` : ''}
                </span>
                <span class="file-actions">
                  {#if isActive(d)}
                    {#if d.state === 'queued'}
                      <button class="icon-btn small" title="Try now" aria-label="Try {d.name} now" onclick={() => run(() => api.retryDownload(d.id))}><Icon name="retry" size={17} /></button>
                    {/if}
                    <button class="icon-btn small" title="Cancel" aria-label="Cancel {d.name}" onclick={() => run(() => api.cancelDownload(d.id))}><Icon name="close" size={17} /></button>
                  {:else}
                    {#if d.state !== 'completed'}
                      <button class="icon-btn small" title="Retry" aria-label="Retry {d.name}" onclick={() => run(() => api.retryDownload(d.id))}><Icon name="retry" size={17} /></button>
                    {/if}
                    <button class="icon-btn small" title="Remove from list" aria-label="Remove {d.name}" onclick={() => run(() => api.removeDownload(d.id))}><Icon name="trash" size={17} /></button>
                  {/if}
                </span>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {:else}
      <div class="card">
        <div class="empty">
          {#if downloads.size}
            <span class="empty-icon"><Icon name="filter" size={26} /></span>
            <strong>No {show} downloads</strong>
            <button class="small" onclick={() => (show = 'all')}>Show all</button>
          {:else}
            <span class="empty-icon"><Icon name="download" size={26} /></span>
            <strong>Nothing downloading</strong>
            <p>Find an album in <a href={href(['search'])}>Search</a> and download a folder. Progress shows up here.</p>
          {/if}
        </div>
      </div>
    {/each}
  </div>
</div>

<style>
  .strong {
    color: var(--text);
  }
  .group {
    overflow: hidden;
  }
  .group-head {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 12px 12px 10px 8px;
  }
  .caret :global(.icon) {
    transition: transform 0.15s;
  }
  .caret.open :global(.icon) {
    transform: rotate(90deg);
  }
  .group-title {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    flex: 1;
    min-width: 0;
  }
  .group-title .name {
    display: block;
    max-width: 100%;
    min-height: 0;
    padding: 0;
    border: 0;
    background: transparent;
    text-align: left;
    font-weight: 600;
    font-size: 15px;
  }
  .group-title .name:hover:not(:disabled) {
    background: transparent;
  }
  .meta-line {
    display: block;
    max-width: 100%;
  }
  .meta-line a {
    color: inherit;
    font-weight: 500;
  }
  .pct {
    flex: none;
    font-size: 13px;
    font-weight: 600;
    color: var(--muted);
    margin-right: 4px;
  }
  .pct.done {
    color: var(--good);
  }
  .seg {
    align-self: flex-start;
  }
  .group-actions {
    display: flex;
    gap: 6px;
  }
  .group-bar {
    height: 3px;
    border-radius: 0;
    margin: 0;
  }
  .group-bar > span {
    border-radius: 0;
  }
  ul.rows > li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 9px 12px 9px 46px;
  }
  ul.rows {
    border-top: 1px solid var(--border);
  }
  .file {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1;
    min-width: 0;
  }
  .fname {
    font-weight: 500;
  }
  .bar.thin {
    height: 3px;
    margin-top: 4px;
  }
  .file-actions {
    display: flex;
    justify-content: flex-end;
    min-width: 64px;
  }
  .meta {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .phone-only {
    display: none;
  }
  @media (max-width: 720px) {
    ul.rows > li {
      padding: 10px 6px 10px 14px;
      gap: 6px;
    }
    .phone-only {
      display: inline-flex;
      flex: none;
    }
    .wide-only {
      display: none;
    }
    .file-actions {
      min-width: 0;
    }
  }
  @media (max-width: 420px) {
    .hide-xs {
      display: none;
    }
  }
</style>

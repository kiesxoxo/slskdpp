<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Download, type ServerState, type Upload } from './lib/api';
  import { downloads, isActive, loadDownloads, removeDownload, updateDownload } from './lib/downloads.svelte';
  import { isUploading, loadUploads, uploads } from './lib/uploads.svelte';
  import { subscribe } from './lib/events';
  import { speed } from './lib/format';
  import { route, href } from './lib/router.svelte';
  import { session } from './lib/session.svelte';
  import Icon, { type IconName } from './lib/Icon.svelte';
  import Logo from './lib/Logo.svelte';
  import ThemeSwitch from './lib/ThemeSwitch.svelte';
  import Toaster from './lib/Toaster.svelte';
  import Login from './routes/Login.svelte';
  import Search from './routes/Search.svelte';
  import Browse from './routes/Browse.svelte';
  import Downloads from './routes/Downloads.svelte';
  import Uploads from './routes/Uploads.svelte';
  import Shares from './routes/Shares.svelte';
  import Wishlist from './routes/Wishlist.svelte';
  import Settings from './routes/Settings.svelte';

  onMount(async () => {
    try {
      session.user = (await api.session()).username;
    } catch {
      session.user = null;
    }
    session.checked = true;
  });

  // Global events only (search 0): the connection indicator and
  // transfers stay live on every page.
  $effect(() => {
    if (!session.user) return;
    const loadAll = () => {
      loadDownloads();
      loadUploads();
    };
    loadAll();
    return subscribe(
      0,
      {
        'server.state': (s: ServerState) => {
          session.connected = s.connected;
          session.soulseekUser = s.username;
          session.version = s.version;
          session.stateKnown = true;
        },
        download: (d: Download) => updateDownload(d),
        'download.removed': (e: { id: number }) => removeDownload(e.id),
        upload: (u: Upload) => uploads.set(u.id, u),
        'upload.removed': (e: { id: number }) => uploads.delete(e.id),
      },
      loadAll,
    );
  });

  let transfers = $derived.by(() => {
    let down = 0;
    let downRate = 0;
    for (const d of downloads.values()) {
      if (!isActive(d)) continue;
      down++;
      downRate += d.speed;
    }
    let up = 0;
    let upRate = 0;
    for (const u of uploads.values()) {
      if (!isUploading(u)) continue;
      up++;
      upRate += u.speed;
    }
    return { down, downRate, up, upRate };
  });

  async function logout() {
    await api.logout().catch(() => {});
    session.user = null;
  }

  interface NavItem {
    page: string;
    label: string;
    icon: IconName;
    count?: number;
  }
  let sections: { title: string; items: NavItem[] }[] = $derived([
    {
      title: 'Find',
      items: [
        { page: 'search', label: 'Search', icon: 'search' },
        { page: 'browse', label: 'Browse', icon: 'browse' },
        { page: 'wishlist', label: 'Wishlist', icon: 'wishlist' },
      ],
    },
    {
      title: 'Transfers',
      items: [
        { page: 'downloads', label: 'Downloads', icon: 'download', count: transfers.down },
        { page: 'uploads', label: 'Uploads', icon: 'upload', count: transfers.up },
      ],
    },
    {
      title: 'Library',
      items: [
        { page: 'shares', label: 'Shares', icon: 'shares' },
        { page: 'settings', label: 'Settings', icon: 'settings' },
      ],
    },
  ]);
  let nav = $derived(sections.flatMap((s) => s.items));
  // Phones show these as tabs and the rest under More.
  const tabPages = ['search', 'browse', 'downloads', 'uploads'];
  let tabs = $derived(tabPages.map((p) => nav.find((n) => n.page === p)!));
  let rest = $derived(nav.filter((n) => !tabPages.includes(n.page)));

  let page = $derived(nav.some((n) => n.page === route.path[0]) ? route.path[0] : 'search');
  let title = $derived(nav.find((n) => n.page === page)?.label ?? 'slskdpp');
  let inMore = $derived(rest.some((n) => n.page === page));

  $effect(() => {
    const n = transfers.down;
    document.title = `${n ? `(${n}) ` : ''}${title} · slskdpp`;
  });

  let moreOpen = $state(false);
  $effect(() => {
    route.path; // close the sheet on navigation
    moreOpen = false;
  });

  let offline = $derived(session.stateKnown && !session.connected);
</script>

{#if !session.checked}
  <div class="boot"><Logo size={48} /></div>
{:else if !session.user}
  <Login />
{:else}
  <div class="shell">
    <aside class="sidebar">
      <a class="brand" href={href(['search'])}>
        <Logo size={30} />
        <span class="label">slskdpp</span>
      </a>
      <nav aria-label="Main">
        {#each sections as sec (sec.title)}
          <div class="nav-title label">{sec.title}</div>
          {#each sec.items as n (n.page)}
            <a href={href([n.page])} class:active={page === n.page} title={n.label} aria-current={page === n.page ? 'page' : undefined}>
              <Icon name={n.icon} />
              <span class="label">{n.label}</span>
              {#if n.count}<span class="count">{n.count}</span>{/if}
            </a>
          {/each}
        {/each}
      </nav>
      <div class="foot">
        <div class="status" title={session.connected ? `Connected to Soulseek as ${session.soulseekUser}` : 'Not connected to the Soulseek server'}>
          <span class="avatar" class:off={!session.connected}>
            {(session.soulseekUser || '?').slice(0, 1).toUpperCase()}
            <span class="dot" class:good={session.connected} class:bad={!session.connected}></span>
          </span>
          <span class="who label">
            <span class="ellipsis name">{session.soulseekUser || 'Soulseek'}</span>
            <span class="ellipsis muted state">{session.connected ? 'Online' : session.stateKnown ? 'Offline, reconnecting' : 'Connecting…'}</span>
          </span>
        </div>
        <div class="rates label" aria-label="Transfer speeds">
          <a href={href(['downloads'])} title="{transfers.down} downloading"><Icon name="download" size={15} /> <span class="num">{transfers.downRate ? speed(transfers.downRate) : '—'}</span></a>
          <a href={href(['uploads'])} title="{transfers.up} uploading"><Icon name="upload" size={15} /> <span class="num">{transfers.upRate ? speed(transfers.upRate) : '—'}</span></a>
        </div>
        <div class="foot-row">
          <div class="label-only"><ThemeSwitch /></div>
          <button class="icon-btn logout" onclick={logout} title="Log out" aria-label="Log out">
            <Icon name="logout" size={18} />
          </button>
        </div>
      </div>
    </aside>

    <header class="topbar">
      <Logo size={28} />
      <h1>{title}</h1>
      <span class="conn-pill" class:off={!session.connected} title={session.connected ? `Connected as ${session.soulseekUser}` : 'Not connected'}>
        <span class="dot" class:good={session.connected} class:bad={!session.connected}></span>
        {session.connected ? 'Online' : 'Offline'}
      </span>
    </header>

    <main>
      {#if offline}
        <div class="offline" role="status">
          <Icon name="alert" size={16} />
          <span>Not connected to the Soulseek server. Reconnecting…</span>
        </div>
      {/if}
      <div class="page-wrap">
        {#if page === 'downloads'}
          <Downloads />
        {:else if page === 'uploads'}
          <Uploads />
        {:else if page === 'shares'}
          <Shares />
        {:else if page === 'wishlist'}
          <Wishlist />
        {:else if page === 'settings'}
          <Settings />
        {:else if page === 'browse'}
          {#key route.path[1]}
            <Browse user={route.path[1] ?? ''} folder={route.query.get('folder') ?? ''} />
          {/key}
        {:else}
          {#key route.path[1]}
            <Search id={route.path[1] ? Number(route.path[1]) : null} />
          {/key}
        {/if}
      </div>
    </main>

    <nav class="tabbar" aria-label="Main">
      {#each tabs as n (n.page)}
        <a href={href([n.page])} class:active={page === n.page} aria-current={page === n.page ? 'page' : undefined}>
          <span class="tab-icon">
            <Icon name={n.icon} size={22} />
            {#if n.count}<span class="count">{n.count}</span>{/if}
          </span>
          {n.label}
        </a>
      {/each}
      <button class:active={inMore || moreOpen} onclick={() => (moreOpen = !moreOpen)} aria-expanded={moreOpen}>
        <span class="tab-icon"><Icon name="more" size={22} /></span>
        More
      </button>
    </nav>

    {#if moreOpen}
      <div class="sheet-backdrop" onclick={() => (moreOpen = false)} role="presentation"></div>
      <div class="more" role="dialog" aria-label="More">
        <div class="grabber"></div>
        {#each rest as n (n.page)}
          <a href={href([n.page])} class:active={page === n.page}>
            <Icon name={n.icon} />
            <span>{n.label}</span>
            <Icon name="chevron" size={18} />
          </a>
        {/each}
        <div class="more-row">
          <span class="muted small">Theme</span>
          <ThemeSwitch labels />
        </div>
        <div class="more-row">
          <span class="conn-line">
            <span class="dot" class:good={session.connected} class:bad={!session.connected}></span>
            {session.connected ? `Connected as ${session.soulseekUser}` : 'Not connected'}
          </span>
          <button class="small" onclick={logout}><Icon name="logout" size={16} /> Log out</button>
        </div>
      </div>
    {/if}
  </div>
  <Toaster />
{/if}

<style>
  .boot {
    display: grid;
    place-items: center;
    height: 100%;
    animation: pulse 1.4s ease-in-out infinite;
  }
  .shell {
    display: grid;
    grid-template-columns: 232px minmax(0, 1fr);
    grid-template-rows: minmax(0, 1fr);
    height: 100dvh;
  }

  /* Sidebar (tablets and up). */
  .sidebar {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 16px 12px 12px;
    background: var(--surface);
    border-right: 1px solid var(--border);
    min-height: 0;
    overflow-y: auto;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 2px 8px 10px;
    color: var(--text);
    font-weight: 700;
    font-size: 17px;
    letter-spacing: -0.02em;
  }
  .brand:hover {
    text-decoration: none;
  }
  .sidebar nav {
    display: flex;
    flex-direction: column;
    gap: 1px;
    flex: 1;
  }
  .nav-title {
    padding: 14px 10px 5px;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--faint);
  }
  .sidebar nav a {
    display: flex;
    align-items: center;
    gap: 11px;
    padding: 8px 10px;
    border-radius: var(--radius);
    color: var(--muted);
    font-weight: 500;
  }
  .sidebar nav a:hover {
    text-decoration: none;
    background: var(--surface-2);
    color: var(--text);
  }
  .sidebar nav a.active {
    background: var(--accent-soft);
    color: var(--accent-soft-text);
  }
  .sidebar nav a .label {
    flex: 1;
  }
  .count {
    min-width: 20px;
    padding: 0 6px;
    border-radius: 999px;
    background: var(--accent);
    color: var(--accent-text);
    font-size: 11.5px;
    font-weight: 700;
    line-height: 20px;
    text-align: center;
    font-variant-numeric: tabular-nums;
  }
  .foot {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 12px 4px 0;
    border-top: 1px solid var(--border);
  }
  .status {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
  }
  .avatar {
    position: relative;
    display: grid;
    place-items: center;
    flex: none;
    width: 34px;
    height: 34px;
    border-radius: 10px;
    background: var(--accent-soft);
    color: var(--accent-soft-text);
    font-weight: 700;
  }
  .avatar.off {
    background: var(--surface-3);
    color: var(--muted);
  }
  .avatar .dot {
    position: absolute;
    right: -2px;
    bottom: -2px;
    width: 10px;
    height: 10px;
    box-shadow: 0 0 0 2px var(--surface);
  }
  .who {
    display: flex;
    flex-direction: column;
    min-width: 0;
    line-height: 1.3;
  }
  .who .name {
    font-weight: 600;
  }
  .who .state {
    font-size: 12px;
  }
  .rates {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 6px;
  }
  .rates a {
    display: flex;
    align-items: center;
    gap: 5px;
    padding: 5px 8px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--muted);
    font-size: 12px;
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
  }
  .rates a:hover {
    text-decoration: none;
    color: var(--text);
  }
  .foot-row {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .label-only {
    flex: 1;
  }
  .logout {
    flex: none;
  }

  main {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    overflow: clip; /* pages scroll inside; never let focus scroll the shell */
  }
  .page-wrap {
    flex: 1;
    min-height: 0;
  }
  .offline {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    padding: 7px 16px;
    background: var(--warn-soft);
    color: var(--warn);
    font-size: 13px;
    font-weight: 500;
    border-bottom: 1px solid color-mix(in srgb, var(--warn) 25%, transparent);
  }

  /* Phone chrome, hidden on larger screens. */
  .topbar,
  .tabbar {
    display: none;
  }

  /* Tablets: an icon rail. */
  @media (max-width: 1100px) and (min-width: 721px) {
    .shell {
      grid-template-columns: 68px minmax(0, 1fr);
    }
    .sidebar {
      padding: 14px 10px;
      align-items: center;
    }
    .brand {
      padding: 2px 0 10px;
    }
    .sidebar .label,
    .label-only {
      display: none;
    }
    .sidebar nav {
      align-items: center;
    }
    .sidebar nav a {
      position: relative;
      justify-content: center;
      width: 46px;
      height: 42px;
      padding: 0;
    }
    .sidebar nav a + .nav-title {
      display: block;
      width: 28px;
      height: 1px;
      padding: 0;
      margin: 8px 0;
      background: var(--border);
      font-size: 0;
    }
    .sidebar nav .count {
      position: absolute;
      top: 2px;
      right: 0;
      min-width: 17px;
      line-height: 17px;
      font-size: 10.5px;
      padding: 0 4px;
    }
    .foot {
      align-items: center;
      padding: 12px 0 0;
    }
    .foot-row {
      justify-content: center;
    }
  }

  /* Phones: top bar, content, bottom tabs. */
  @media (max-width: 720px) {
    .shell {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto minmax(0, 1fr) auto;
    }
    .sidebar {
      display: none;
    }
    .topbar {
      display: flex;
      align-items: center;
      gap: 10px;
      padding: calc(10px + env(safe-area-inset-top)) 14px 10px;
      background: var(--surface);
      border-bottom: 1px solid var(--border);
    }
    .topbar h1 {
      flex: 1;
      font-size: 18px;
      font-weight: 700;
      letter-spacing: -0.02em;
    }
    .conn-pill {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 3px 10px;
      border-radius: 999px;
      background: var(--good-soft);
      color: var(--good);
      font-size: 12.5px;
      font-weight: 600;
    }
    .conn-pill.off {
      background: var(--bad-soft);
      color: var(--bad);
    }
    .tabbar {
      display: grid;
      grid-template-columns: repeat(5, 1fr);
      padding: 4px 4px env(safe-area-inset-bottom);
      background: var(--surface);
      border-top: 1px solid var(--border);
    }
    .tabbar a,
    .tabbar button {
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 2px;
      min-height: 54px;
      padding: 6px 0 4px;
      border: 0;
      border-radius: var(--radius);
      background: transparent;
      color: var(--muted);
      font-size: 11px;
      font-weight: 600;
    }
    .tabbar a:hover {
      text-decoration: none;
    }
    .tabbar .active {
      color: var(--accent);
    }
    .tab-icon {
      position: relative;
      display: grid;
      place-items: center;
      width: 52px;
      height: 28px;
      border-radius: 999px;
      transition: background 0.15s;
    }
    .tabbar .active .tab-icon {
      background: var(--accent-soft);
    }
    .tab-icon .count {
      position: absolute;
      top: -4px;
      right: 2px;
      min-width: 17px;
      line-height: 17px;
      font-size: 10px;
      padding: 0 4px;
      box-shadow: 0 0 0 2px var(--surface);
    }
    .more {
      position: fixed;
      left: 0;
      right: 0;
      bottom: 0;
      z-index: 41;
      display: flex;
      flex-direction: column;
      gap: 2px;
      padding: 8px 12px calc(16px + env(safe-area-inset-bottom));
      background: var(--surface);
      border-radius: 18px 18px 0 0;
      box-shadow: var(--shadow-lg);
      animation: rise 0.2s ease;
    }
    .grabber {
      width: 40px;
      height: 4px;
      margin: 2px auto 10px;
      border-radius: 999px;
      background: var(--border-strong);
    }
    .more a {
      display: flex;
      align-items: center;
      gap: 12px;
      min-height: 52px;
      padding: 0 10px;
      border-radius: var(--radius);
      color: var(--text);
      font-size: 16px;
      font-weight: 500;
    }
    .more a span {
      flex: 1;
    }
    .more a.active {
      background: var(--accent-soft);
      color: var(--accent-soft-text);
    }
    .more a:hover {
      text-decoration: none;
    }
    .more-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      padding: 12px 10px 0;
      margin-top: 8px;
      border-top: 1px solid var(--border);
    }
    .conn-line {
      display: flex;
      align-items: center;
      gap: 8px;
      color: var(--muted);
      font-size: 14px;
    }
  }
</style>

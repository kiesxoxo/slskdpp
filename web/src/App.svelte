<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Download, type ServerState, type Upload } from './lib/api';
  import { downloads, isActive, loadDownloads, removeDownload, updateDownload } from './lib/downloads.svelte';
  import { isUploading, loadUploads, uploads } from './lib/uploads.svelte';
  import { subscribe } from './lib/events';
  import { route, href } from './lib/router.svelte';
  import { session } from './lib/session.svelte';
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
  // downloads stay live on every page.
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
        },
        download: (d: Download) => updateDownload(d),
        'download.removed': (e: { id: number }) => removeDownload(e.id),
        upload: (u: Upload) => uploads.set(u.id, u),
        'upload.removed': (e: { id: number }) => uploads.delete(e.id),
      },
      loadAll,
    );
  });

  let activeDownloads = $derived([...downloads.values()].filter(isActive).length);
  let activeUploads = $derived([...uploads.values()].filter(isUploading).length);

  async function logout() {
    await api.logout().catch(() => {});
    session.user = null;
  }

  const pages = ['browse', 'downloads', 'uploads', 'shares', 'wishlist', 'settings'];
  let page = $derived(pages.includes(route.path[0]) ? route.path[0] : 'search');
</script>

{#if !session.checked}
  <div class="boot muted">Loading…</div>
{:else if !session.user}
  <Login />
{:else}
  <div class="shell">
    <header>
      <a class="brand" href={href(['search'])}>slskdpp</a>
      <nav>
        <a href={href(['search'])} class:active={page === 'search'}>Search</a>
        <a href={href(['browse'])} class:active={page === 'browse'}>Browse</a>
        <a href={href(['downloads'])} class:active={page === 'downloads'}>
          Downloads{#if activeDownloads}<span class="count">{activeDownloads}</span>{/if}
        </a>
        <a href={href(['uploads'])} class:active={page === 'uploads'}>
          Uploads{#if activeUploads}<span class="count">{activeUploads}</span>{/if}
        </a>
        <a href={href(['wishlist'])} class:active={page === 'wishlist'}>Wishlist</a>
        <a href={href(['shares'])} class:active={page === 'shares'}>Shares</a>
      </nav>
      <span class="status" title={session.connected ? 'Connected to the Soulseek server' : 'Not connected to the Soulseek server'}>
        <span class="dot" class:good={session.connected} class:bad={!session.connected}></span>
        <span class="ellipsis">{session.connected ? session.soulseekUser : 'offline'}</span>
      </span>
      <a class="gear" href={href(['settings'])} class:active={page === 'settings'} title="Settings" aria-label="Settings">⚙</a>
      <button class="ghost" onclick={logout}>Log out</button>
    </header>
    <main>
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
    </main>
  </div>
{/if}

<style>
  .boot {
    display: grid;
    place-items: center;
    height: 100%;
  }
  .shell {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  header {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 8px 16px;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
  }
  .brand {
    font-weight: 700;
    font-size: 16px;
    color: var(--text);
    letter-spacing: -0.01em;
  }
  nav {
    display: flex;
    gap: 4px;
    flex: 1;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }
  nav a {
    white-space: nowrap;
  }
  nav a {
    color: var(--muted);
    padding: 6px 10px;
    border-radius: var(--radius);
  }
  nav a:hover {
    text-decoration: none;
    background: var(--surface-2);
  }
  .gear {
    color: var(--muted);
    font-size: 18px;
    padding: 2px 6px;
    border-radius: var(--radius);
  }
  .gear:hover,
  .gear.active {
    color: var(--text);
    background: var(--surface-2);
    text-decoration: none;
  }
  .count {
    margin-left: 6px;
    padding: 0 6px;
    border-radius: 999px;
    background: var(--accent);
    color: var(--accent-text);
    font-size: 11.5px;
    font-weight: 600;
  }
  nav a.active {
    color: var(--text);
    background: var(--surface-2);
    font-weight: 600;
  }
  .status {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--muted);
    font-size: 13px;
    max-width: 30vw;
  }
  main {
    flex: 1;
    min-height: 0;
  }
  @media (max-width: 600px) {
    header {
      flex-wrap: wrap;
      gap: 4px 8px;
      padding: 8px;
    }
    .status {
      margin-left: auto;
    }
    nav {
      order: 2;
      flex-basis: 100%;
    }
    nav a {
      padding: 6px 8px;
    }
    .brand,
    .status .ellipsis {
      display: none;
    }
    header button {
      white-space: nowrap;
      padding: 7px 8px;
    }
  }
</style>

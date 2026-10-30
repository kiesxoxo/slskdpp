<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type ServerState } from './lib/api';
  import { subscribe } from './lib/events';
  import { route, href } from './lib/router.svelte';
  import { session } from './lib/session.svelte';
  import Login from './routes/Login.svelte';
  import Search from './routes/Search.svelte';
  import Browse from './routes/Browse.svelte';

  onMount(async () => {
    try {
      session.user = (await api.session()).username;
    } catch {
      session.user = null;
    }
    session.checked = true;
  });

  // Global events only (search 0): keeps the connection indicator live.
  $effect(() => {
    if (!session.user) return;
    return subscribe(0, {
      'server.state': (s: ServerState) => {
        session.connected = s.connected;
        session.soulseekUser = s.username;
      },
    });
  });

  async function logout() {
    await api.logout().catch(() => {});
    session.user = null;
  }

  let page = $derived(route.path[0] === 'browse' ? 'browse' : 'search');
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
      </nav>
      <span class="status" title={session.connected ? 'Connected to the Soulseek server' : 'Not connected to the Soulseek server'}>
        <span class="dot" class:good={session.connected} class:bad={!session.connected}></span>
        <span class="ellipsis">{session.connected ? session.soulseekUser : 'offline'}</span>
      </span>
      <button class="ghost" onclick={logout}>Log out</button>
    </header>
    <main>
      {#if page === 'browse'}
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
      gap: 8px;
      padding: 8px;
    }
    .brand {
      display: none;
    }
  }
</style>

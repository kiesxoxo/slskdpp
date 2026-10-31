<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorText, type ApiKey, type HookResult } from '../lib/api';
  import { ago } from '../lib/format';
  import { session } from '../lib/session.svelte';
  import Icon from '../lib/Icon.svelte';
  import PageHead from '../lib/PageHead.svelte';
  import ThemeSwitch from '../lib/ThemeSwitch.svelte';

  let keys = $state<ApiKey[]>([]);
  let hooks = $state<{ on_download: string; on_folder: string; webhook: string } | null>(null);
  let results = $state<HookResult[] | null>(null);
  let testing = $state(false);
  let error = $state('');
  let name = $state('');
  let created = $state<{ name: string; key: string } | null>(null);
  let copied = $state(false);

  async function load() {
    try {
      [keys, hooks] = await Promise.all([api.apiKeys(), api.hooks()]);
    } catch (e) {
      error = errorText(e);
    }
  }
  onMount(load);

  async function create(e: SubmitEvent) {
    e.preventDefault();
    error = '';
    try {
      const k = await api.createApiKey(name);
      created = { name: k.name, key: k.key };
      copied = false;
      name = '';
      await load();
    } catch (err) {
      error = errorText(err);
    }
  }

  async function revoke(k: ApiKey) {
    error = '';
    try {
      await api.revokeApiKey(k.id);
      await load();
    } catch (err) {
      error = errorText(err);
    }
  }

  async function copy() {
    if (!created) return;
    try {
      await navigator.clipboard.writeText(created.key);
      copied = true;
    } catch {
      // the key is selectable as well
    }
  }

  async function test() {
    testing = true;
    error = '';
    try {
      results = await api.testHooks();
    } catch (err) {
      error = errorText(err);
    } finally {
      testing = false;
    }
  }

  let anyHook = $derived(!!hooks && !!(hooks.on_download || hooks.on_folder || hooks.webhook));
</script>

<div class="page-scroll">
  <div class="page-body narrow">
    <PageHead title="Settings" />
    {#if error}<p class="banner" role="alert">{error}</p>{/if}

    <section class="card block">
      <div class="block-head">
        <span class="block-icon"><Icon name="user" /></span>
        <div>
          <h2>Account</h2>
          <p class="muted small">Your Soulseek identity and this web login</p>
        </div>
      </div>
      <dl>
        <dt>Soulseek user</dt>
        <dd><strong>{session.soulseekUser || '…'}</strong></dd>
        <dt>Server</dt>
        <dd>
          <span class="badge {session.connected ? 'good' : 'bad'}">
            <span class="dot" class:good={session.connected} class:bad={!session.connected}></span>
            {session.connected ? 'Connected' : 'Not connected'}
          </span>
        </dd>
        <dt>Web login</dt>
        <dd>{session.user}</dd>
        <dt>Version</dt>
        <dd class="mono">{session.version || '…'}</dd>
      </dl>
    </section>

    <section class="card block">
      <div class="block-head">
        <span class="block-icon"><Icon name="moon" /></span>
        <div>
          <h2>Appearance</h2>
          <p class="muted small">Remembered in this browser</p>
        </div>
      </div>
      <ThemeSwitch labels />
    </section>

    <section class="card block">
      <div class="block-head">
        <span class="block-icon"><Icon name="key" /></span>
        <div>
          <h2>API keys</h2>
          <p class="muted small">
            For scripts and tools like Lidarr or soularr: send a key in an <span class="mono">X-API-Key</span> header. Keys can use
            the whole API except managing keys.
          </p>
        </div>
      </div>
      {#if created}
        <div class="created" role="status">
          <p><strong>“{created.name}” created.</strong> Copy it now; it will not be shown again.</p>
          <div class="row">
            <input class="mono" readonly value={created.key} aria-label="New API key" onfocus={(e) => (e.target as HTMLInputElement).select()} />
            <button onclick={copy}><Icon name={copied ? 'check' : 'copy'} size={17} /> {copied ? 'Copied' : 'Copy'}</button>
          </div>
          <button class="ghost small done" onclick={() => (created = null)}>Done</button>
        </div>
      {/if}
      {#if keys.length}
        <ul class="rows list">
          {#each keys as k (k.id)}
            <li>
              <div class="info">
                <span><strong>{k.name}</strong> <span class="mono muted">{k.prefix}…</span></span>
                <span class="muted small">
                  {#if k.from_config}Set in the config file{:else}Created {ago(k.created)} · last used {ago(k.last_used)}{/if}
                </span>
              </div>
              {#if !k.from_config}<button class="small" onclick={() => revoke(k)}>Revoke</button>{/if}
            </li>
          {/each}
        </ul>
      {/if}
      <form class="row" onsubmit={create}>
        <input placeholder="What the key is for, e.g. lidarr" bind:value={name} aria-label="Key name" />
        <button class="primary" disabled={!name.trim()}><Icon name="plus" size={18} /> Create key</button>
      </form>
    </section>

    <section class="card block">
      <div class="block-head">
        <span class="block-icon"><Icon name="hook" /></span>
        <div>
          <h2>Hooks</h2>
          <p class="muted small">Run when downloads finish. Set them in the config file under <span class="mono">hooks:</span>.</p>
        </div>
      </div>
      {#if hooks}
        <dl>
          <dt>Each file</dt>
          <dd class="mono">{hooks.on_download || '—'}</dd>
          <dt>Each folder</dt>
          <dd class="mono">{hooks.on_folder || '—'}</dd>
          <dt>Webhook</dt>
          <dd class="mono">{hooks.webhook || '—'}</dd>
        </dl>
      {/if}
      <div>
        <button onclick={test} disabled={testing || !anyHook}>{testing ? 'Running…' : 'Send a test event'}</button>
      </div>
      {#if results}
        <ul class="rows list">
          {#each results as r (r.hook)}
            <li>
              <div class="info">
                <span class="result">
                  <span class="badge {r.ok ? 'good' : 'bad'}">{r.ok ? 'OK' : 'Failed'}</span>
                  <strong>{r.hook}</strong>
                </span>
                {#if r.error}<span class="error small">{r.error}</span>{/if}
                {#if r.output}<pre class="mono small">{r.output}</pre>{/if}
              </div>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  </div>
</div>

<style>
  .narrow {
    max-width: 760px;
  }
  .block {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 18px;
  }
  .block-head {
    display: flex;
    gap: 14px;
    align-items: flex-start;
  }
  .block-icon {
    display: grid;
    place-items: center;
    width: 38px;
    height: 38px;
    flex: none;
    border-radius: 11px;
    background: var(--accent-soft);
    color: var(--accent-soft-text);
  }
  .block-head h2 {
    font-size: 16px;
    margin-bottom: 2px;
  }
  .row {
    display: flex;
    gap: 8px;
  }
  .row input {
    flex: 1;
  }
  .created {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 14px;
    border: 1px solid var(--accent);
    background: var(--accent-soft);
    border-radius: var(--radius);
  }
  .done {
    align-self: flex-end;
  }
  .list {
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .list > li {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
  }
  .info {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1;
    min-width: 0;
  }
  .result {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  pre {
    margin: 4px 0 0;
    white-space: pre-wrap;
    word-break: break-word;
    color: var(--muted);
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 8px 18px;
    margin: 0;
    align-items: center;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
    word-break: break-all;
  }
  @media (max-width: 720px) {
    .block {
      padding: 16px 14px;
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
    dl {
      grid-template-columns: 1fr;
      gap: 2px;
    }
    dd {
      margin-bottom: 8px;
    }
  }
</style>

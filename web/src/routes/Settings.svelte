<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorText, type ApiKey, type HookResult } from '../lib/api';
  import { ago } from '../lib/format';
  import { session } from '../lib/session.svelte';

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

<div class="page">
  <div class="content">
    {#if error}<p class="error" role="alert">{error}</p>{/if}

    <section>
      <h2>Account</h2>
      <p>
        Soulseek user <strong>{session.soulseekUser || '…'}</strong> ·
        <span class="dot" class:good={session.connected} class:bad={!session.connected}></span>
        {session.connected ? 'connected' : 'not connected'} · web login <strong>{session.user}</strong>
      </p>
    </section>

    <section>
      <h2>API keys</h2>
      <p class="muted small">
        For scripts and tools like Lidarr: send the key in an <span class="mono">X-API-Key</span> header (or
        <span class="mono">Authorization: Bearer</span>). Keys can use the whole API except managing keys.
      </p>
      {#if created}
        <div class="created" role="status">
          <p><strong>Key “{created.name}” created.</strong> Copy it now; it will not be shown again.</p>
          <div class="row">
            <input class="mono" readonly value={created.key} aria-label="New API key" onfocus={(e) => (e.target as HTMLInputElement).select()} />
            <button onclick={copy}>{copied ? 'Copied' : 'Copy'}</button>
            <button class="ghost" onclick={() => (created = null)}>Done</button>
          </div>
        </div>
      {/if}
      <ul class="list">
        {#each keys as k (k.id)}
          <li>
            <div class="info">
              <span><strong>{k.name}</strong> <span class="mono muted">{k.prefix}…</span></span>
              <span class="muted small">
                {#if k.from_config}set in the config file{:else}created {ago(k.created)} · last used {ago(k.last_used)}{/if}
              </span>
            </div>
            {#if !k.from_config}<button onclick={() => revoke(k)}>Revoke</button>{/if}
          </li>
        {:else}
          <li class="muted">No API keys yet.</li>
        {/each}
      </ul>
      <form class="row" onsubmit={create}>
        <input placeholder="What the key is for, e.g. lidarr" bind:value={name} aria-label="Key name" />
        <button class="primary" disabled={!name.trim()}>Create key</button>
      </form>
    </section>

    <section>
      <h2>Hooks</h2>
      <p class="muted small">Run when downloads finish. Set them in the config file under <span class="mono">hooks:</span>.</p>
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
        <ul class="list">
          {#each results as r (r.hook)}
            <li>
              <div class="info">
                <span><span class="dot" class:good={r.ok} class:bad={!r.ok}></span> <strong>{r.hook}</strong> {r.ok ? 'ran fine' : 'failed'}</span>
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
  .page {
    height: 100%;
    overflow-y: auto;
  }
  .content {
    padding: 16px;
    max-width: 820px;
    display: flex;
    flex-direction: column;
    gap: 28px;
  }
  section {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  h2 {
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--muted);
    margin: 0;
  }
  p {
    margin: 0;
  }
  .small {
    font-size: 12.5px;
  }
  .row {
    display: flex;
    gap: 6px;
  }
  .row input {
    flex: 1;
  }
  .created {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px;
    border: 1px solid var(--accent);
    background: var(--accent-soft);
    border-radius: 10px;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 10px;
  }
  .list > li {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 14px;
  }
  .list > li + li {
    border-top: 1px solid var(--border);
  }
  .info {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1;
    min-width: 0;
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
    gap: 6px 16px;
    margin: 0;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
    word-break: break-all;
  }
</style>

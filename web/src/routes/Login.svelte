<script lang="ts">
  import { api, errorText } from '../lib/api';
  import { session } from '../lib/session.svelte';
  import Logo from '../lib/Logo.svelte';

  let username = $state('');
  let password = $state('');
  let error = $state('');
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      session.user = (await api.login(username, password)).username;
    } catch (err) {
      error = errorText(err);
    } finally {
      busy = false;
    }
  }
</script>

<div class="wrap">
  <form class="card" onsubmit={submit}>
    <div class="head">
      <Logo size={56} />
      <h1>slskdpp</h1>
      <p class="muted">Sign in to your Soulseek client</p>
    </div>
    <label>
      <span>Username</span>
      <input bind:value={username} autocomplete="username" autocapitalize="off" spellcheck="false" required />
    </label>
    <label>
      <span>Password</span>
      <input type="password" bind:value={password} autocomplete="current-password" required />
    </label>
    {#if error}<p class="banner" role="alert">{error}</p>{/if}
    <button class="primary" disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</button>
    <p class="hint muted small">The username is <code>slskdpp</code> unless <code>web.username</code> says otherwise.</p>
  </form>
</div>

<style>
  .wrap {
    display: grid;
    place-items: center;
    min-height: 100dvh;
    padding: 20px;
    background:
      radial-gradient(60rem 30rem at 50% -10%, color-mix(in srgb, var(--accent) 16%, transparent), transparent 70%),
      var(--bg);
  }
  form {
    display: flex;
    flex-direction: column;
    gap: 16px;
    width: min(380px, 100%);
    padding: 32px 28px 28px;
    box-shadow: var(--shadow-lg);
  }
  .head {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    margin-bottom: 8px;
    text-align: center;
  }
  h1 {
    margin-top: 8px;
    font-size: 24px;
    letter-spacing: -0.02em;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 13px;
    font-weight: 500;
    color: var(--muted);
  }
  label input {
    color: var(--text);
  }
  button {
    margin-top: 4px;
    min-height: 42px;
  }
  .hint {
    text-align: center;
  }
  code {
    font-family: var(--mono);
    font-size: 12px;
    padding: 1px 5px;
    border-radius: 5px;
    background: var(--surface-2);
    color: var(--text);
  }
</style>

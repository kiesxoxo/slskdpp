<script lang="ts">
  import { api, errorText } from '../lib/api';
  import { session } from '../lib/session.svelte';

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
  <form onsubmit={submit}>
    <h1>slskdpp</h1>
    <label>
      Username
      <input bind:value={username} autocomplete="username" required />
    </label>
    <label>
      Password
      <input type="password" bind:value={password} autocomplete="current-password" required />
    </label>
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <button class="primary" disabled={busy}>{busy ? 'Logging in…' : 'Log in'}</button>
  </form>
</div>

<style>
  .wrap {
    display: grid;
    place-items: center;
    height: 100%;
    padding: 16px;
  }
  form {
    display: flex;
    flex-direction: column;
    gap: 14px;
    width: min(340px, 100%);
    padding: 28px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 12px;
  }
  h1 {
    margin: 0 0 4px;
    font-size: 22px;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
    color: var(--muted);
    font-size: 13px;
  }
  label input {
    color: var(--text);
    font-size: 14px;
  }
  p {
    margin: 0;
  }
</style>

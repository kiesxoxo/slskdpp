<script lang="ts">
  import { dismiss, toasts } from './toast.svelte';
  import Icon from './Icon.svelte';
</script>

<div class="toaster" aria-live="polite">
  {#each toasts as t (t.id)}
    <div class="toast {t.kind}" role={t.kind === 'error' ? 'alert' : 'status'}>
      <span class="ic"><Icon name={t.kind === 'error' ? 'alert' : t.kind === 'success' ? 'check' : 'info'} size={17} stroke={2} /></span>
      <span class="msg">{t.message}</span>
      {#if t.action}<a href={t.action.href} onclick={() => dismiss(t.id)}>{t.action.label}</a>{/if}
      <button class="icon-btn small" aria-label="Dismiss" onclick={() => dismiss(t.id)}><Icon name="close" size={15} /></button>
    </div>
  {/each}
</div>

<style>
  .toaster {
    position: fixed;
    right: 20px;
    bottom: 20px;
    z-index: 60;
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 8px;
    pointer-events: none;
  }
  .toast {
    display: flex;
    align-items: center;
    gap: 10px;
    max-width: 440px;
    padding: 8px 6px 8px 12px;
    border-radius: 12px;
    border: 1px solid var(--border-strong);
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-lg);
    font-size: 13.5px;
    pointer-events: auto;
    animation: rise 0.18s ease;
  }
  .ic {
    display: grid;
    place-items: center;
    flex: none;
    color: var(--accent);
  }
  .success .ic {
    color: var(--good);
  }
  .error .ic {
    color: var(--bad);
  }
  .msg {
    flex: 1;
    min-width: 0;
  }
  a {
    font-weight: 600;
    white-space: nowrap;
  }
  @media (max-width: 720px) {
    .toaster {
      left: 12px;
      right: 12px;
      bottom: calc(var(--tabbar) + 12px);
      align-items: stretch;
    }
    .toast {
      max-width: none;
    }
  }
</style>

<script lang="ts">
  import Icon from './Icon.svelte';
  import { setTheme, theme, type Theme } from './theme.svelte';

  let { labels = false }: { labels?: boolean } = $props();

  const choices: { value: Theme; label: string; icon: 'system' | 'sun' | 'moon' }[] = [
    { value: 'system', label: 'System', icon: 'system' },
    { value: 'light', label: 'Light', icon: 'sun' },
    { value: 'dark', label: 'Dark', icon: 'moon' },
  ];
</script>

<div class="switch" class:labels role="radiogroup" aria-label="Theme">
  {#each choices as c (c.value)}
    <button
      type="button"
      role="radio"
      aria-checked={theme.value === c.value}
      class:on={theme.value === c.value}
      title="{c.label} theme"
      onclick={() => setTheme(c.value)}
    >
      <Icon name={c.icon} size={16} />
      {#if labels}<span>{c.label}</span>{/if}
    </button>
  {/each}
</div>

<style>
  .switch {
    display: flex;
    padding: 3px;
    gap: 2px;
    border-radius: var(--radius);
    background: var(--surface-2);
    border: 1px solid var(--border);
  }
  button {
    flex: 1;
    min-height: 30px;
    padding: 0 8px;
    border: 0;
    border-radius: calc(var(--radius) - 3px);
    background: transparent;
    color: var(--muted);
    font-size: 13px;
  }
  button:hover:not(.on) {
    background: transparent;
    color: var(--text);
  }
  button.on {
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-sm);
  }
  .labels button {
    min-height: 36px;
  }
</style>

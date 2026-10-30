<script lang="ts">
  // A page's title, summary and actions. On phones the top bar shows
  // the title, so only the rest appears.
  import type { Snippet } from 'svelte';

  let { title, summary, actions }: { title: string; summary?: Snippet; actions?: Snippet } = $props();
</script>

<header class="page-head">
  <div class="text">
    <h1>{title}</h1>
    {#if summary}<div class="summary">{@render summary()}</div>{/if}
  </div>
  {#if actions}<div class="actions">{@render actions()}</div>{/if}
</header>

<style>
  .page-head {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 12px 16px;
    flex-wrap: wrap;
  }
  .text {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  h1 {
    font-size: 24px;
    font-weight: 700;
    letter-spacing: -0.025em;
  }
  .summary {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--muted);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  @media (max-width: 720px) {
    h1 {
      display: none;
    }
    .page-head {
      align-items: center;
    }
    .actions {
      width: 100%;
    }
    .actions :global(button) {
      flex: 1;
    }
  }
</style>

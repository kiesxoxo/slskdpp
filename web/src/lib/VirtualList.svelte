<script lang="ts" generics="T">
  // Renders only the rows in view, so lists of many thousands stay fast.
  // Rows have a fixed height.
  import type { Snippet } from 'svelte';

  let {
    items,
    rowHeight,
    row,
    empty,
  }: { items: T[]; rowHeight: number; row: Snippet<[T, number]>; empty?: Snippet } = $props();

  const overscan = 6;
  let viewport: HTMLDivElement;
  let scrollTop = $state(0);
  let height = $state(0);
  let start = $derived(Math.max(0, Math.floor(scrollTop / rowHeight) - overscan));
  let end = $derived(Math.min(items.length, Math.ceil((scrollTop + height) / rowHeight) + overscan));
</script>

<div class="viewport" bind:this={viewport} bind:clientHeight={height} onscroll={() => (scrollTop = viewport.scrollTop)}>
  {#if items.length === 0 && empty}
    {@render empty()}
  {:else}
    <div class="spacer" style:height="{items.length * rowHeight}px">
      {#each items.slice(start, end) as item, i (start + i)}
        <div class="row" style:top="{(start + i) * rowHeight}px" style:height="{rowHeight}px">
          {@render row(item, start + i)}
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .viewport {
    height: 100%;
    overflow-y: auto;
    overscroll-behavior: contain;
  }
  .spacer {
    position: relative;
  }
  .row {
    position: absolute;
    left: 0;
    right: 0;
  }
</style>

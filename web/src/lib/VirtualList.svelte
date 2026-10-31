<script lang="ts" generics="T">
  // Renders only the rows in view, so lists of many thousands stay fast.
  // Each row's height is fixed: one number for all rows, or a function of
  // the item. An optional header stays at the top while scrolling.
  import type { Snippet } from 'svelte';

  let {
    items,
    rowHeight,
    row,
    empty,
    header,
    key,
  }: {
    items: T[];
    rowHeight: number | ((item: T) => number);
    row: Snippet<[T, number]>;
    empty?: Snippet;
    header?: Snippet;
    key?: (item: T, index: number) => unknown;
  } = $props();

  const overscan = 6;
  let viewport: HTMLDivElement;
  let scrollTop = $state(0);
  let height = $state(0);
  let headHeight = $state(0);

  // Row tops, when heights vary: offsets[i] is where row i starts, and
  // offsets[items.length] is the total height.
  let offsets = $derived.by(() => {
    const h = rowHeight;
    if (typeof h === 'number') return null;
    const out = new Float64Array(items.length + 1);
    for (let i = 0; i < items.length; i++) out[i + 1] = out[i] + h(items[i]);
    return out;
  });

  function top(i: number): number {
    return offsets ? offsets[i] : i * (rowHeight as number);
  }

  // indexAt returns the row at y, measured from the top of the rows.
  function indexAt(y: number): number {
    if (!offsets) return Math.floor(y / (rowHeight as number));
    let lo = 0;
    let hi = items.length;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (offsets[mid] <= y) lo = mid;
      else hi = mid - 1;
    }
    return lo;
  }

  let total = $derived(top(items.length));
  let start = $derived(Math.max(0, indexAt(scrollTop - headHeight) - overscan));
  let end = $derived(Math.min(items.length, indexAt(scrollTop - headHeight + height) + 1 + overscan));
  let visible = $derived(items.slice(start, end).map((item, i) => ({ item, i: start + i })));

  // scrollToIndex scrolls just enough to show row i below the header.
  export function scrollToIndex(i: number) {
    if (!viewport || i < 0 || i >= items.length) return;
    const t = top(i);
    const b = top(i + 1);
    if (viewport.scrollTop > t) viewport.scrollTop = t;
    else if (viewport.scrollTop < b + headHeight - height) viewport.scrollTop = b + headHeight - height;
    scrollTop = viewport.scrollTop; // render the row now, not on the next scroll event
  }
</script>

<div class="viewport" bind:this={viewport} bind:clientHeight={height} onscroll={() => (scrollTop = viewport.scrollTop)}>
  {#if header}
    <div class="header" bind:clientHeight={headHeight}>{@render header()}</div>
  {/if}
  {#if items.length === 0 && empty}
    {@render empty()}
  {:else}
    <div class="spacer" style:height="{total}px">
      {#each visible as v (key ? key(v.item, v.i) : v.i)}
        <div class="row" style:top="{top(v.i)}px" style:height="{top(v.i + 1) - top(v.i)}px">
          {@render row(v.item, v.i)}
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
  .header {
    position: sticky;
    top: 0;
    z-index: 2;
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

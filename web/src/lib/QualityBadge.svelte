<script lang="ts">
  // The audio quality of a folder as a pill coloured by tier.
  import type { FileInfo } from './api';
  import { qualityDetail, qualityLabel, qualityTier, summarize } from './quality';

  let { files }: { files: FileInfo[] } = $props();
  let s = $derived(summarize(files));
</script>

<span class="q {qualityTier(s)}" title={qualityDetail(s)}>{qualityLabel(s)}</span>

<style>
  .q {
    display: inline-flex;
    align-items: center;
    max-width: 100%;
    padding: 1px 8px;
    border-radius: 6px;
    font-size: 11.5px;
    font-weight: 650;
    line-height: 19px;
    letter-spacing: 0.01em;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-variant-numeric: tabular-nums;
    background: var(--surface-2);
    color: var(--muted);
  }
  .hires {
    background: var(--q-hires-soft);
    color: var(--q-hires);
  }
  .lossless {
    background: var(--accent-soft);
    color: var(--accent-soft-text);
  }
  .high {
    background: var(--q-high-soft);
    color: var(--q-high);
  }
  .low {
    background: var(--q-low-soft);
    color: var(--q-low);
  }
</style>

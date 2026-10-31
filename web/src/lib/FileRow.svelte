<script lang="ts">
  // One file of a remote folder: a checkbox to pick it, its name, quality,
  // length and size, and its download state or a button to download it.
  import type { FileInfo } from './api';
  import { downloadOf, isActive, stateLabel } from './downloads.svelte';
  import { duration, size } from './format';
  import { fileQuality, isAudio } from './quality';
  import DownloadButton from './DownloadButton.svelte';
  import Icon from './Icon.svelte';

  let {
    file,
    username,
    checked = false,
    ontoggle,
    indent = 0,
  }: { file: FileInfo; username: string; checked?: boolean; ontoggle?: () => void; indent?: number } = $props();

  let d = $derived(downloadOf(username, file.path));
  let q = $derived(fileQuality(file));
  let len = $derived(duration(file.duration));
</script>

<div class="file" class:extra={!isAudio(file)} class:checked style:--indent="{indent}px">
  {#if ontoggle}
    <input type="checkbox" {checked} onchange={ontoggle} aria-label="Select {file.name}" />
  {:else}
    <span></span>
  {/if}
  <span class="name" title={file.name}>
    <span class="ellipsis">{file.name}</span>
    <span class="sub">{[q, len, size(file.size)].filter(Boolean).join(' · ')}</span>
  </span>
  <span class="col q">{q}</span>
  <span class="col len">{len}</span>
  <span class="col sz">{size(file.size)}</span>
  <span class="act">
    {#if d && isActive(d)}
      <span class="state active" title={stateLabel[d.state]}><Icon name="download" size={16} /></span>
    {:else if d?.state === 'completed'}
      <span class="state done" title="Downloaded"><Icon name="check" size={17} /></span>
    {:else}
      <DownloadButton compact {username} files={[{ path: file.path, size: file.size }]} label="Download {file.name}" />
    {/if}
  </span>
</div>

<style>
  .file {
    display: grid;
    grid-template-columns: 28px minmax(0, 1fr) 72px 56px 72px 36px;
    align-items: center;
    gap: 0 8px;
    min-height: 34px;
    padding: 0 16px 0 var(--indent);
    font-size: 13px;
  }
  .file:hover {
    background: var(--row-hover);
  }
  .file.checked {
    background: color-mix(in srgb, var(--accent-soft) 60%, transparent);
  }
  .name {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .extra .name,
  .extra .col {
    color: var(--faint);
  }
  .sub {
    display: none;
    font-size: 12px;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .col {
    text-align: right;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .act {
    display: flex;
    justify-content: flex-end;
  }
  .state {
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
  }
  .state.active {
    color: var(--accent);
    animation: pulse 1.4s ease-in-out infinite;
  }
  .state.done {
    color: var(--good);
  }
  @media (max-width: 720px) {
    .file {
      grid-template-columns: 30px minmax(0, 1fr) 40px;
      min-height: 52px;
      padding-right: 6px;
      font-size: 14px;
    }
    .col {
      display: none;
    }
    .sub {
      display: block;
    }
  }
</style>

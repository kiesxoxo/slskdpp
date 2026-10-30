// Live download state, kept current by the global event stream.
import { SvelteMap } from 'svelte/reactivity';
import { api, type Download, type DownloadState } from './api';

export const downloads = new SvelteMap<number, Download>();

export async function loadDownloads() {
  try {
    const list = await api.downloads();
    downloads.clear();
    for (const d of list) downloads.set(d.id, d);
  } catch {
    // shown as empty; the next event or reload fills it in
  }
}

export function updateDownload(d: Download) {
  downloads.set(d.id, d);
}

export function removeDownload(id: number) {
  downloads.delete(id);
}

export const activeStates: DownloadState[] = ['queued', 'requested', 'queued_remote', 'initializing', 'transferring'];

export function isActive(d: Download): boolean {
  return activeStates.includes(d.state);
}

export const stateLabel: Record<DownloadState, string> = {
  queued: 'Queued',
  requested: 'Requesting',
  queued_remote: 'In their queue',
  initializing: 'Starting',
  transferring: 'Downloading',
  completed: 'Done',
  failed: 'Failed',
  cancelled: 'Cancelled',
};

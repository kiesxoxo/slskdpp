// Live download state, kept current by the global event stream.
import { SvelteMap } from 'svelte/reactivity';
import { api, type Download, type DownloadState } from './api';

export const downloads = new SvelteMap<number, Download>();

// The newest download of each user's remote file, so search results and
// browse can show what is already queued or done.
const byFile = new SvelteMap<string, number>();
const fileKey = (username: string, path: string) => username + '\u0000' + path;

function index(d: Download) {
  const k = fileKey(d.username, d.filename);
  const cur = byFile.get(k);
  if (cur === undefined || cur <= d.id) byFile.set(k, d.id);
}

function unindex(d: Download) {
  const k = fileKey(d.username, d.filename);
  if (byFile.get(k) === d.id) byFile.delete(k);
}

export async function loadDownloads() {
  try {
    const list = await api.downloads();
    downloads.clear();
    byFile.clear();
    for (const d of list) {
      downloads.set(d.id, d);
      index(d);
    }
  } catch {
    // shown as empty; the next event or reload fills it in
  }
}

export function updateDownload(d: Download) {
  const old = downloads.get(d.id);
  if (old && (old.username !== d.username || old.filename !== d.filename)) unindex(old);
  downloads.set(d.id, d);
  index(d);
}

export function removeDownload(id: number) {
  const old = downloads.get(id);
  if (old) unindex(old);
  downloads.delete(id);
}

// downloadOf returns the download of a user's remote file, if any.
export function downloadOf(username: string, path: string): Download | undefined {
  const id = byFile.get(fileKey(username, path));
  return id === undefined ? undefined : downloads.get(id);
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

// eta estimates seconds left at the current speed, or 0 when unknown.
export function eta(remaining: number, bytesPerSecond: number): number {
  return bytesPerSecond > 0 && remaining > 0 ? Math.ceil(remaining / bytesPerSecond) : 0;
}

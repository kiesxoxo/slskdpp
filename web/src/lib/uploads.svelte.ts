// Live upload state, kept current by the global event stream.
import { SvelteMap } from 'svelte/reactivity';
import { api, type Upload } from './api';

export const uploads = new SvelteMap<number, Upload>();
export const uploadSlots = $state({ slots: 0 });

export async function loadUploads() {
  try {
    const { status, uploads: list } = await api.uploads();
    uploadSlots.slots = status.slots;
    uploads.clear();
    for (const u of list) uploads.set(u.id, u);
  } catch {
    // filled in by the next event or reload
  }
}

export const isUploading = (u: Upload) => u.state === 'initializing' || u.state === 'transferring';

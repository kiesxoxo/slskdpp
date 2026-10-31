import { session } from './session.svelte';

export interface FileInfo {
  path: string;
  name: string;
  size: number;
  ext: string;
  bitrate?: number;
  duration?: number;
  sample_rate?: number;
  bit_depth?: number;
  vbr?: boolean;
}

export interface Hit {
  user: string;
  folder: string;
  name: string;
  files: FileInfo[];
  file_count: number;
  formats: string[];
  avg_bitrate?: number;
  lossless: boolean;
  total_size: number;
  slot_free: boolean;
  queue_length: number;
  upload_speed: number;
  score: number;
  album_key: string;
}

export interface SearchView {
  id: number;
  query: string;
  started: string;
  ends: string;
  responses: number;
  files: number;
  folders: number;
  active: boolean;
}

export interface Dir {
  name: string;
  files: FileInfo[];
}

export interface BrowseResult {
  user: string;
  dirs: Dir[] | null;
  private_dirs: Dir[] | null;
}

export interface ServerState {
  connected: boolean;
  username: string;
  version: string;
}

export type DownloadState =
  | 'queued'
  | 'requested'
  | 'queued_remote'
  | 'initializing'
  | 'transferring'
  | 'completed'
  | 'failed'
  | 'cancelled';

export interface Download {
  id: number;
  username: string;
  filename: string;
  name: string;
  folder: string;
  size: number;
  bytes: number;
  state: DownloadState;
  error?: string;
  attempts: number;
  sources: number;
  searching?: boolean;
  queue_place?: number;
  speed: number;
  next_attempt?: string;
  created: string;
  updated: string;
  completed?: string;
}

export interface FileRequest {
  path: string;
  size: number;
}

export type UploadState = 'queued' | 'initializing' | 'transferring' | 'completed' | 'failed' | 'cancelled';

export interface Upload {
  id: number;
  username: string;
  filename: string;
  name: string;
  folder: string;
  size: number;
  bytes: number;
  state: UploadState;
  error?: string;
  place?: number;
  speed: number;
  created: string;
  started?: string;
  completed?: string;
}

export interface UploadStatus {
  slots: number;
  active: number;
  queued: number;
  slot_free: boolean;
  avg_speed: number;
}

export interface ShareStats {
  folders: number;
  files: number;
  bytes: number;
  scanning: boolean;
  last_scan?: string;
}

export interface ShareRoot {
  alias: string;
  path: string;
  from_config: boolean;
  folders: number;
  files: number;
  bytes: number;
}

export interface LocalFolders {
  path: string;
  parent: string;
  folders: string[];
}

export interface SearchFilter {
  formats?: string[];
  min_bitrate?: number;
  free_slot_only?: boolean;
  min_files?: number;
  exclude_users?: string[];
}

export interface WishlistEntry {
  id: number;
  query: string;
  filter: SearchFilter;
  auto_download: boolean;
  enabled: boolean;
  created: string;
  last_run?: string;
  last_search?: number;
  matches: number;
  running?: boolean;
  got_user?: string;
  got_folder?: string;
  got_at?: string;
}

export interface ApiKey {
  id: number;
  name: string;
  prefix: string;
  created?: string;
  last_used?: string;
  from_config?: boolean;
}

export interface HookResult {
  hook: string;
  ok: boolean;
  error?: string;
  output?: string;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch('/api/v1' + path, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  });
  if (!res.ok) {
    if (res.status === 401 && path !== '/session') session.user = null;
    let msg = res.statusText;
    try {
      msg = (await res.json()).error ?? msg;
    } catch {
      // not JSON
    }
    throw new ApiError(res.status, msg);
  }
  return res.status === 204 ? (undefined as T) : res.json();
}

const user = (u: string) => '/users/' + encodeURIComponent(u);

export const api = {
  session: () => request<{ username: string }>('GET', '/session'),
  login: (username: string, password: string) =>
    request<{ username: string }>('POST', '/session', { username, password }),
  logout: () => request<void>('DELETE', '/session'),

  searches: () => request<SearchView[]>('GET', '/searches'),
  search: (id: number) => request<SearchView>('GET', `/searches/${id}`),
  startSearch: (query: string) => request<SearchView>('POST', '/searches', { query }),
  deleteSearch: (id: number) => request<void>('DELETE', `/searches/${id}`),
  hits: (id: number) => request<Hit[]>('GET', `/searches/${id}/hits`),

  browse: (u: string) => request<BrowseResult>('GET', user(u) + '/browse'),

  downloads: () => request<Download[]>('GET', '/downloads'),
  download: (username: string, files: FileRequest[]) =>
    request<Download[]>('POST', '/downloads', { username, files }),
  downloadFolder: (username: string, folder: string, files: FileRequest[]) =>
    request<Download[]>('POST', '/downloads/folder', { username, folder, files }),
  cancelDownload: (id: number) => request<void>('POST', `/downloads/${id}/cancel`),
  retryDownload: (id: number) => request<void>('POST', `/downloads/${id}/retry`),
  removeDownload: (id: number) => request<void>('DELETE', `/downloads/${id}`),
  clearDownloads: (states: DownloadState[]) => request<{ removed: number }>('POST', '/downloads/clear', { states }),

  uploads: () => request<{ status: UploadStatus; uploads: Upload[] }>('GET', '/uploads'),
  cancelUpload: (id: number) => request<void>('POST', `/uploads/${id}/cancel`),
  removeUpload: (id: number) => request<void>('DELETE', `/uploads/${id}`),
  clearUploads: () => request<{ removed: number }>('POST', '/uploads/clear'),

  shares: () => request<{ stats: ShareStats; roots: ShareRoot[] }>('GET', '/shares'),
  addShare: (path: string, alias: string) => request<ShareRoot>('POST', '/shares', { path, alias }),
  removeShare: (alias: string) => request<void>('DELETE', '/shares/' + encodeURIComponent(alias)),
  rescanShares: () => request<void>('POST', '/shares/rescan'),
  localFolders: (path: string) => request<LocalFolders>('GET', '/folders?path=' + encodeURIComponent(path)),

  wishlist: () => request<{ interval_seconds: number; entries: WishlistEntry[] }>('GET', '/wishlist'),
  addWishlist: (query: string, filter: SearchFilter, auto_download: boolean) =>
    request<WishlistEntry>('POST', '/wishlist', { query, filter, auto_download }),
  updateWishlist: (id: number, change: Partial<Pick<WishlistEntry, 'enabled' | 'auto_download' | 'filter' | 'query'>>) =>
    request<WishlistEntry>('PATCH', `/wishlist/${id}`, change),
  removeWishlist: (id: number) => request<void>('DELETE', `/wishlist/${id}`),
  searchWishlist: (id: number) => request<void>('POST', `/wishlist/${id}/search`),

  apiKeys: () => request<ApiKey[]>('GET', '/apikeys'),
  createApiKey: (name: string) => request<ApiKey & { key: string }>('POST', '/apikeys', { name }),
  revokeApiKey: (id: number) => request<void>('DELETE', `/apikeys/${id}`),

  hooks: () => request<{ on_download: string; on_folder: string; webhook: string }>('GET', '/hooks'),
  testHooks: () => request<HookResult[]>('POST', '/hooks/test'),
};

export function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

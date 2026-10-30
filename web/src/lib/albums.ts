// Client-side filtering, ranking and album grouping of streamed folder
// hits. This mirrors internal/search so results can update live without
// refetching; the server's /albums endpoint gives the same answer.
import type { Hit } from './api';

export interface Filter {
  formats: string[];
  minBitrate: number;
  freeOnly: boolean;
  minFiles: number;
  exclude: string[];
}

export interface Album {
  key: string; // the first source's user and folder, stable while it stays first
  name: string;
  file_count: number;
  total_size: number;
  sources: Hit[];
}

export const hitKey = (h: Hit) => h.user + '\u0000' + h.folder;

export function matches(h: Hit, f: Filter): boolean {
  if (f.freeOnly && !h.slot_free) return false;
  if (h.file_count < f.minFiles) return false;
  if (f.minBitrate > 0 && !h.lossless && (h.avg_bitrate ?? 0) < f.minBitrate) return false;
  if (f.exclude.includes(h.user)) return false;
  if (f.formats.length > 0 && !f.formats.some((x) => h.formats.includes(x))) return false;
  return true;
}

export function rank(hits: Hit[]): Hit[] {
  return hits.sort(
    (a, b) =>
      b.score - a.score ||
      b.upload_speed - a.upload_speed ||
      a.user.localeCompare(b.user) ||
      a.folder.localeCompare(b.folder),
  );
}

const sizeTolerance = 0.03;

function closeSize(a: number, b: number): boolean {
  const hi = Math.max(a, b);
  return hi === 0 || (hi - Math.min(a, b)) / hi <= sizeTolerance;
}

// groupAlbums dedupes ranked hits into albums, ordered by best source.
export function groupAlbums(ranked: Hit[]): Album[] {
  const albums: Album[] = [];
  const index = new Map<string, Album[]>();
  for (const h of ranked) {
    let album: Album | undefined;
    if (!h.album_key.startsWith('|')) {
      album = index.get(h.album_key)?.find((a) => closeSize(a.total_size, h.total_size));
    }
    if (!album) {
      album = { key: hitKey(h), name: h.name || h.folder, file_count: h.file_count, total_size: h.total_size, sources: [] };
      albums.push(album);
      const list = index.get(h.album_key) ?? [];
      list.push(album);
      index.set(h.album_key, list);
    }
    album.sources.push(h);
  }
  return albums;
}

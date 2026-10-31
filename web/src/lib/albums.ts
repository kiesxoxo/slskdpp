// Client-side filtering, ranking and album grouping of streamed folder
// hits. matches, rank and groupAlbums mirror internal/search so results
// can update live without refetching; the server's /albums endpoint gives
// the same answer for the filters it knows. The text filter, quality
// tiers and sorting exist only here.
import type { Hit, SearchFilter } from './api';
import { losslessExts, qualityRank, summarize } from './quality';

export type QualityFilter = 'any' | '192' | '256' | '320' | 'lossless' | 'hires';

export interface Filter {
  formats: string[];
  quality: QualityFilter;
  freeOnly: boolean;
  minFiles: number;
  exclude: string[];
  text: string;
}

export const emptyFilter = (): Filter => ({ formats: [], quality: 'any', freeOnly: false, minFiles: 0, exclude: [], text: '' });

export interface Album {
  key: string; // the first source's user and folder, stable while it stays first
  name: string;
  file_count: number;
  total_size: number;
  sources: Hit[];
}

export const hitKey = (h: Hit) => h.user + '\u0000' + h.folder;

function minBitrate(q: QualityFilter): number {
  return q === '192' || q === '256' || q === '320' ? Number(q) : 0;
}

// textTerms splits a filter like `live -remaster` into words that must
// appear and words that must not.
function textTerms(text: string): { want: string[]; not: string[] } {
  const want: string[] = [];
  const not: string[] = [];
  for (const t of text.toLowerCase().split(/\s+/)) {
    if (t.length > 1 && t.startsWith('-')) not.push(t.slice(1));
    else if (t && t !== '-') want.push(t);
  }
  return { want, not };
}

const haystacks = new WeakMap<Hit, string>();

function haystack(h: Hit): string {
  let s = haystacks.get(h);
  if (s === undefined) {
    s = (h.user + '\n' + h.folder + '\n' + h.files.map((f) => f.name).join('\n')).toLowerCase();
    haystacks.set(h, s);
  }
  return s;
}

// compile returns a predicate for f, doing the per-filter work once.
export function compile(f: Filter): (h: Hit) => boolean {
  const br = minBitrate(f.quality);
  const terms = textTerms(f.text);
  const exclude = new Set(f.exclude.map((u) => u.toLowerCase()));
  return (h) => {
    if (f.freeOnly && !h.slot_free) return false;
    if (h.file_count < f.minFiles) return false;
    if (br > 0 && !h.lossless && (h.avg_bitrate ?? 0) < br) return false;
    if (exclude.size && exclude.has(h.user.toLowerCase())) return false;
    if (f.formats.length > 0 && !f.formats.some((x) => h.formats.includes(x))) return false;
    if (f.quality === 'lossless' && !h.lossless) return false;
    if (f.quality === 'hires') {
      const s = summarize(h.files);
      if (!h.lossless || !(s.bitDepth > 16 || s.sampleRate > 48000)) return false;
    }
    if (terms.want.length || terms.not.length) {
      const text = haystack(h);
      if (!terms.want.every((t) => text.includes(t))) return false;
      if (terms.not.some((t) => text.includes(t))) return false;
    }
    return true;
  };
}

// activeFilters counts the filters that narrow the results.
export function activeFilters(f: Filter): number {
  return (
    f.formats.length +
    Number(f.quality !== 'any') +
    Number(f.freeOnly) +
    Number(f.minFiles > 0) +
    Number(f.exclude.length > 0) +
    Number(f.text.trim() !== '')
  );
}

// toSearchFilter is the part of f the server understands, for the
// wishlist. Lossless tiers become a list of lossless formats.
export function toSearchFilter(f: Filter): SearchFilter {
  let formats = f.formats;
  if ((f.quality === 'lossless' || f.quality === 'hires') && !formats.some((x) => losslessExts.has(x))) {
    formats = [...formats, 'flac', 'wav', 'alac', 'ape', 'wv', 'aiff'];
  }
  return {
    formats,
    min_bitrate: minBitrate(f.quality),
    min_files: f.minFiles || 0,
    free_slot_only: f.freeOnly,
    exclude_users: f.exclude,
  };
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

// ungrouped makes every folder hit its own album, for a per-user view.
export function ungrouped(ranked: Hit[]): Album[] {
  return ranked.map((h) => ({ key: hitKey(h), name: h.name || h.folder, file_count: h.file_count, total_size: h.total_size, sources: [h] }));
}

export type SortKey = 'relevance' | 'name' | 'quality' | 'tracks' | 'size' | 'length' | 'user' | 'speed' | 'queue';

// The natural direction of each sort: 1 ascending, -1 descending.
export const sortDefaults: Record<SortKey, 1 | -1> = {
  relevance: 1,
  name: 1,
  quality: -1,
  tracks: -1,
  size: -1,
  length: -1,
  user: 1,
  speed: -1,
  queue: 1,
};

// Queue position for sorting: a free slot comes before any queue.
const queueOf = (h: Hit) => (h.slot_free ? -1 : h.queue_length);

function metric(a: Album, key: SortKey): number | string {
  const best = a.sources[0];
  switch (key) {
    case 'name':
      return a.name.toLowerCase();
    case 'user':
      return best.user.toLowerCase();
    case 'quality':
      return qualityRank(summarize(best.files));
    case 'tracks':
      return summarize(best.files).audio || a.file_count;
    case 'size':
      return a.total_size;
    case 'length':
      return summarize(best.files).duration;
    case 'speed':
      return Math.max(...a.sources.map((s) => s.upload_speed));
    case 'queue':
      return Math.min(...a.sources.map(queueOf));
    default:
      return 0;
  }
}

// sortAlbums orders albums by key; ties keep the ranked order. With
// 'relevance' the ranked order is kept as is.
export function sortAlbums(albums: Album[], key: SortKey, dir: 1 | -1): Album[] {
  if (key === 'relevance') return dir === 1 ? albums : [...albums].reverse();
  const keyed = albums.map((a, i) => ({ a, i, m: metric(a, key) }));
  keyed.sort((x, y) => {
    const c = typeof x.m === 'string' ? x.m.localeCompare(y.m as string) : (x.m as number) - (y.m as number);
    return c * dir || x.i - y.i;
  });
  return keyed.map((k) => k.a);
}

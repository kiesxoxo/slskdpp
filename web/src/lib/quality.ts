// Describes the audio in a folder at a glance: format, bit depth and
// sample rate or bitrate, total length, and a quality tier for colour.
import type { FileInfo } from './api';

export const losslessExts = new Set(['flac', 'wav', 'aif', 'aiff', 'ape', 'wv', 'alac', 'dsf', 'dff']);
const lossyExts = new Set(['mp3', 'ogg', 'oga', 'opus', 'm4a', 'aac', 'wma', 'mpc', 'mp2']);
const artExts = new Set(['jpg', 'jpeg', 'png', 'gif', 'webp', 'bmp']);

export const isAudioExt = (ext: string) => losslessExts.has(ext) || lossyExts.has(ext);
export const isAudio = (f: FileInfo) => isAudioExt(f.ext);

export type Tier = 'hires' | 'lossless' | 'high' | 'mid' | 'low' | 'unknown';

export interface Summary {
  audio: number; // audio files
  duration: number; // seconds, over audio files
  format: string; // most common audio format, lowercase; '' when none
  formats: string[]; // all extensions, most common first
  lossless: boolean;
  bitDepth: number; // most common among audio files, 0 when unknown
  sampleRate: number; // Hz, most common, 0 when unknown
  bitrate: number; // average kbps over audio files that have one
  vbr: boolean;
  art: boolean;
  cue: boolean;
  log: boolean;
}

function mostCommon<T>(counts: Map<T, number>, none: T): T {
  let best = none;
  let n = 0;
  for (const [k, c] of counts) {
    if (c > n) {
      best = k;
      n = c;
    }
  }
  return best;
}

function bump<T>(m: Map<T, number>, k: T) {
  m.set(k, (m.get(k) ?? 0) + 1);
}

const memo = new WeakMap<FileInfo[], Summary>();

// summarize describes a folder's files. Results are cached per array, so
// callers must not mutate it afterwards.
export function summarize(files: FileInfo[]): Summary {
  let s = memo.get(files);
  if (s) return s;
  const exts = new Map<string, number>();
  const audioExts = new Map<string, number>();
  const depths = new Map<number, number>();
  const rates = new Map<number, number>();
  let audio = 0;
  let duration = 0;
  let brSum = 0;
  let brN = 0;
  let vbr = 0;
  let art = false;
  let cue = false;
  let log = false;
  for (const f of files) {
    if (f.ext) bump(exts, f.ext);
    if (artExts.has(f.ext)) art = true;
    else if (f.ext === 'cue') cue = true;
    else if (f.ext === 'log') log = true;
    if (!isAudio(f)) continue;
    audio++;
    bump(audioExts, f.ext);
    duration += f.duration ?? 0;
    if (f.bit_depth) bump(depths, f.bit_depth);
    if (f.sample_rate) bump(rates, f.sample_rate);
    if (f.bitrate) {
      brSum += f.bitrate;
      brN++;
    }
    if (f.vbr) vbr++;
  }
  const format = mostCommon(audioExts, '');
  s = {
    audio,
    duration,
    format,
    formats: [...exts].sort((a, b) => b[1] - a[1]).map(([e]) => e),
    lossless: losslessExts.has(format) || (format === 'm4a' && depths.size > 0),
    bitDepth: mostCommon(depths, 0),
    sampleRate: mostCommon(rates, 0),
    bitrate: brN ? Math.round(brSum / brN) : 0,
    vbr: vbr * 2 > audio,
    art,
    cue,
    log,
  };
  memo.set(files, s);
  return s;
}

// khz formats a sample rate: 44100 → "44.1", 96000 → "96".
export function khz(hz: number): string {
  return String(Math.round(hz / 100) / 10);
}

function formatName(ext: string, lossless: boolean): string {
  if (ext === 'dsf' || ext === 'dff') return 'DSD';
  if (ext === 'm4a') return lossless ? 'ALAC' : 'AAC';
  if (ext === 'aif') return 'AIFF';
  if (ext === 'oga') return 'OGG';
  return ext.toUpperCase();
}

// qualityLabel is a short label like "FLAC 24/96", "MP3 320" or
// "MP3 245 VBR".
export function qualityLabel(s: Summary): string {
  if (!s.format) return s.formats[0]?.toUpperCase() ?? '—';
  const name = formatName(s.format, s.lossless);
  if (s.lossless) {
    if (s.bitDepth && s.sampleRate) return `${name} ${s.bitDepth}/${khz(s.sampleRate)}`;
    return name;
  }
  if (!s.bitrate) return name;
  return `${name} ${s.bitrate}${s.vbr ? ' VBR' : ''}`;
}

// qualityDetail is a longer description for tooltips.
export function qualityDetail(s: Summary): string {
  const parts: string[] = [];
  if (s.lossless) {
    parts.push('Lossless');
    if (s.bitDepth) parts.push(`${s.bitDepth}-bit`);
    if (s.sampleRate) parts.push(`${khz(s.sampleRate)} kHz`);
  } else if (s.bitrate) {
    parts.push(`${s.vbr ? 'Variable bitrate, about ' : ''}${s.bitrate} kbps`);
  }
  if (s.formats.length) parts.push(s.formats.join(', '));
  return parts.join(' · ');
}

export function qualityTier(s: Summary): Tier {
  if (!s.format) return 'unknown';
  if (s.lossless) return s.bitDepth > 16 || s.sampleRate > 48000 ? 'hires' : 'lossless';
  if (!s.bitrate) return 'unknown';
  if (s.bitrate >= 220) return 'high';
  if (s.bitrate >= 160) return 'mid';
  return 'low';
}

// Rank of a folder's quality, for sorting: higher is better.
export function qualityRank(s: Summary): number {
  const tier = { hires: 5, lossless: 4, high: 3, mid: 2, low: 1, unknown: 0 }[qualityTier(s)];
  return tier * 100000 + (s.lossless ? s.bitDepth * 1000 + s.sampleRate / 1000 : s.bitrate);
}

// fileQuality describes one file: "24/96" for lossless audio with a
// known depth, else its bitrate like "320k".
export function fileQuality(f: FileInfo): string {
  if (f.bit_depth && f.sample_rate) return `${f.bit_depth}/${khz(f.sample_rate)}`;
  if (!f.bitrate) return '';
  return `${f.vbr ? '~' : ''}${f.bitrate}k`;
}

const discFolder = /^(cd|disc|disk|dvd)\s*\d+$/i;

// folderParts splits a remote folder into a display name and up to two
// parent folders ("Music › Portishead"), leaving out the share prefix.
// Disc folders ("CD1") take their album's name.
export function folderParts(folder: string): { name: string; parent: string } {
  const parts = folder.split(/[\\/]/).filter(Boolean);
  if (parts.length > 1 && parts[0].startsWith('@@')) parts.shift();
  let name = parts.pop() ?? folder;
  if (discFolder.test(name.trim()) && parts.length) name = `${parts.pop()} · ${name}`;
  return { name, parent: parts.slice(-2).join(' › ') };
}

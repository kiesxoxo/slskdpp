import type { Hit } from './api';

export function size(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = 'KMGTPE';
  let i = -1;
  do {
    n /= 1024;
    i++;
  } while (n >= 1024 && i < units.length - 1);
  return `${n.toFixed(n < 10 ? 1 : 0)} ${units[i]}B`;
}

export function speed(bytesPerSecond: number): string {
  return bytesPerSecond > 0 ? size(bytesPerSecond) + '/s' : '—';
}

export function duration(seconds?: number): string {
  if (!seconds) return '';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = String(seconds % 60).padStart(2, '0');
  return h ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${m}:${s}`;
}

export function quality(h: Hit): string {
  if (h.lossless) return 'lossless';
  return h.avg_bitrate ? `${h.avg_bitrate} kbps` : '? kbps';
}

export function plural(n: number, word: string): string {
  return `${n.toLocaleString()} ${word}${n === 1 ? '' : 's'}`;
}

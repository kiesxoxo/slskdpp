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

// ago describes how long ago an ISO time was, roughly.
export function ago(iso?: string): string {
  if (!iso) return 'never';
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
}

export function plural(n: number, word: string): string {
  return `${n.toLocaleString()} ${word}${n === 1 ? '' : 's'}`;
}

// left describes a number of seconds still to go, roughly.
export function left(seconds: number): string {
  if (seconds <= 0) return '';
  if (seconds < 60) return `${seconds} s left`;
  if (seconds < 3600) return `${Math.round(seconds / 60)} min left`;
  const h = Math.floor(seconds / 3600);
  return `${h} h ${Math.round((seconds % 3600) / 60)} min left`;
}

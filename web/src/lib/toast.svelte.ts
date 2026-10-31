// Short-lived notices in a corner of the screen, for the outcome of an
// action that has no better place to show it.

export interface Toast {
  id: number;
  message: string;
  kind: 'info' | 'success' | 'error';
  action?: { label: string; href: string };
}

export const toasts = $state<Toast[]>([]);
let next = 1;

export function toast(t: Omit<Toast, 'id' | 'kind'> & { kind?: Toast['kind']; timeout?: number }) {
  const id = next++;
  const kind = t.kind ?? 'info';
  toasts.push({ id, kind, message: t.message, action: t.action });
  while (toasts.length > 4) toasts.shift();
  setTimeout(() => dismiss(id), t.timeout ?? (kind === 'error' ? 8000 : 4500));
}

export function dismiss(id: number) {
  const i = toasts.findIndex((x) => x.id === id);
  if (i >= 0) toasts.splice(i, 1);
}

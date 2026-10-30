// The color theme: follow the system, or force light or dark. Remembered
// per browser; public/theme.js applies it before the first paint.

export type Theme = 'system' | 'light' | 'dark';

const key = 'slskdpp-theme';

function saved(): Theme {
  try {
    const t = localStorage.getItem(key);
    if (t === 'light' || t === 'dark') return t;
  } catch {
    // storage may be blocked
  }
  return 'system';
}

export const theme = $state({ value: saved() });

const systemDark = window.matchMedia('(prefers-color-scheme: dark)');

function apply() {
  const root = document.documentElement;
  if (theme.value === 'system') delete root.dataset.theme;
  else root.dataset.theme = theme.value;
  const dark = theme.value === 'dark' || (theme.value === 'system' && systemDark.matches);
  // Browser chrome (address bar, status bar) follows the page.
  for (const m of document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]')) {
    m.content = dark ? '#0e1015' : '#f5f6f8';
  }
}

export function setTheme(t: Theme) {
  theme.value = t;
  try {
    if (t === 'system') localStorage.removeItem(key);
    else localStorage.setItem(key, t);
  } catch {
    // the choice still applies to this page
  }
  apply();
}

systemDark.addEventListener('change', apply);
apply();

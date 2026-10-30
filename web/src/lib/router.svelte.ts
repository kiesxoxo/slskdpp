// A tiny hash router: #/search/12 or #/browse/bob?folder=...

export const route = $state({ path: [] as string[], query: new URLSearchParams() });

function parse() {
  const hash = location.hash.replace(/^#\/?/, '');
  const [p, q = ''] = hash.split('?');
  route.path = p.split('/').filter(Boolean).map(decodeURIComponent);
  route.query = new URLSearchParams(q);
}

window.addEventListener('hashchange', parse);
parse();

export function href(parts: string[], query?: Record<string, string>): string {
  let h = '#/' + parts.map(encodeURIComponent).join('/');
  if (query) h += '?' + new URLSearchParams(query).toString();
  return h;
}

export function navigate(parts: string[], query?: Record<string, string>) {
  location.hash = href(parts, query);
}

import { api } from './api';

type Handlers = Record<string, (data: any) => void>;

// subscribe opens the live event stream. search narrows it to one
// search's events (0 for global events only). onResync runs after the
// stream reconnects, since events may have been missed meanwhile.
export function subscribe(search: number, handlers: Handlers, onResync?: () => void): () => void {
  const es = new EventSource(`/api/v1/events?search=${search}`);
  let opened = false;
  es.addEventListener('open', () => {
    if (opened) onResync?.();
    opened = true;
  });
  for (const [type, fn] of Object.entries(handlers)) {
    es.addEventListener(type, (e) => fn(JSON.parse((e as MessageEvent).data)));
  }
  es.addEventListener('error', () => {
    // The browser retries on its own unless the server refused the stream,
    // which usually means the session expired; checking it shows the login.
    if (es.readyState === EventSource.CLOSED) api.session().catch(() => {});
  });
  return () => es.close();
}

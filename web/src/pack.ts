// One instance can serve several packs. The console remembers which one the
// person chose and sends it with every request; no choice means the server's
// default pack.
const KEY = 'zyntra.pack';

export function getPack(): string {
  try {
    return localStorage.getItem(KEY) ?? '';
  } catch {
    return '';
  }
}

export function setPack(id: string) {
  try {
    if (id) localStorage.setItem(KEY, id);
    else localStorage.removeItem(KEY);
  } catch {
    /* storage can be blocked; the choice then lasts for this page load only */
  }
}

/** The query string that tells EventSource, which cannot set headers, which pack to watch. */
export function packQuery(): string {
  const id = getPack();
  return id ? `?pack=${encodeURIComponent(id)}` : '';
}

import { useCallback, useEffect, useRef, useState } from 'react';
import { api, type Pulse } from './api';
import { packQuery } from './pack';

export function useApi<T>(path: string | null, refreshMs = 0) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string>('');
  const [loading, setLoading] = useState(true);
  const seq = useRef(0);

  const load = useCallback(async () => {
    if (!path) return;
    const n = ++seq.current;
    try {
      const v = await api<T>(path);
      if (n === seq.current) {
        setData(v);
        setError('');
      }
    } catch (e) {
      if (n === seq.current) setError((e as Error).message);
    } finally {
      if (n === seq.current) setLoading(false);
    }
  }, [path]);

  useEffect(() => {
    load();
    if (!refreshMs) return;
    const t = setInterval(load, refreshMs);
    return () => clearInterval(t);
  }, [load, refreshMs]);

  return { data, error, loading, reload: load };
}

export function usePulse(enabled = true) {
  const [pulse, setPulse] = useState<Pulse | null>(null);
  useEffect(() => {
    if (!enabled) return;
    const es = new EventSource(`/api/v1/events${packQuery()}`, { withCredentials: true });
    es.addEventListener('pulse', (e) => {
      try {
        setPulse(JSON.parse((e as MessageEvent).data));
      } catch {
        /* ignore */
      }
    });
    return () => es.close();
  }, [enabled]);
  return pulse;
}

const OWNER_KEY = 'zyntra.owner';

/** useOwner keeps one owner filter across the Gaps and Plan pages. */
export function useOwner(): [string, (o: string) => void] {
  const [owner, set] = useState(() => localStorage.getItem(OWNER_KEY) ?? '');
  const update = useCallback((o: string) => {
    if (o) localStorage.setItem(OWNER_KEY, o);
    else localStorage.removeItem(OWNER_KEY);
    set(o);
  }, []);
  return [owner, update];
}

export const withOwner = (path: string, owner: string) => (owner ? `${path}?owner=${encodeURIComponent(owner)}` : path);

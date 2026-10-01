import { useCallback, useEffect, useRef, useState } from 'react';
import { api, type Pulse } from './api';

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

export function usePulse() {
  const [pulse, setPulse] = useState<Pulse | null>(null);
  useEffect(() => {
    const es = new EventSource('/api/v1/events', { withCredentials: true });
    es.addEventListener('pulse', (e) => {
      try {
        setPulse(JSON.parse((e as MessageEvent).data));
      } catch {
        /* ignore */
      }
    });
    return () => es.close();
  }, []);
  return pulse;
}

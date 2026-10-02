import { vi } from 'vitest';

type Reply = unknown | ((init?: RequestInit, url?: string) => unknown);
export interface Call {
  method: string;
  path: string;
  body?: unknown;
}

/**
 * Replace fetch with a table of "METHOD /path" replies. A reply may be a
 * function; a Response status other than 200 is given as {status, body}. An
 * unlisted route answers 404 so a page that calls something unexpected fails
 * loudly. Query strings are ignored when matching.
 */
export function mockApi(routes: Record<string, Reply>) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      // A server decodes the path before routing; match the same way.
      const path = decodeURIComponent(url.pathname);
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, path, body });
      const hit = routes[`${method} ${path}`];
      if (hit === undefined) return new Response(JSON.stringify({ error: 'not mocked: ' + method + ' ' + path }), { status: 404 });
      const reply = typeof hit === 'function' ? (hit as (i?: RequestInit, u?: string) => unknown)(init, path) : hit;
      if (reply && typeof reply === 'object' && 'status' in reply && 'body' in reply) {
        const r = reply as { status: number; body: unknown };
        return new Response(JSON.stringify(r.body), { status: r.status });
      }
      return new Response(JSON.stringify(reply), { status: 200 });
    }),
  );
  return calls;
}

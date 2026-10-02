import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { api, type ServedPack } from '../api';
import Nav from '../components/Nav';
import { getPack, packQuery, setPack } from '../pack';

const packs: ServedPack[] = [
  { id: 'gpu', title: 'GPU cluster', default: true },
  { id: 'shop', title: 'Counter and stock' },
];

const nav = (p?: ServedPack[]) =>
  render(<Nav page="overview" setPage={() => {}} theme="light" onToggleTheme={() => {}} pulse={null} operator="ann" packs={p} />);

let reload: ReturnType<typeof vi.fn>;
beforeEach(() => {
  localStorage.clear();
  reload = vi.fn();
  vi.stubGlobal('location', { ...window.location, hash: '', reload });
});
afterEach(() => vi.unstubAllGlobals());

describe('pack selection', () => {
  it('sends the chosen pack with every request, and nothing when none is chosen', async () => {
    const seen: (string | null)[] = [];
    vi.stubGlobal('fetch', vi.fn(async (_u: string, init?: RequestInit) => {
      seen.push(new Headers(init?.headers).get('X-Zyntra-Pack'));
      return new Response('{}', { status: 200 });
    }));
    await api('/api/v1/graph');
    setPack('shop');
    await api('/api/v1/graph');
    expect(seen).toEqual([null, 'shop']);
    expect(packQuery()).toBe('?pack=shop');
    setPack('');
    expect(packQuery()).toBe('');
  });

  it('falls back to the default pack when the remembered one is no longer served', async () => {
    setPack('gone');
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'unknown pack' }), { status: 404 })));
    await expect(api('/api/v1/graph')).rejects.toThrow('unknown pack');
    expect(getPack()).toBe('');
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('does not reload for an ordinary 404', async () => {
    setPack('shop');
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'not found' }), { status: 404 })));
    await expect(api('/api/v1/nothing')).rejects.toThrow('not found');
    expect(getPack()).toBe('shop');
    expect(reload).not.toHaveBeenCalled();
  });
});

describe('pack switcher', () => {
  it('is hidden for a single pack, or when the list is not known', () => {
    nav([packs[0]]);
    expect(screen.queryByRole('combobox', { name: 'Pack' })).toBeNull();
  });

  it('is hidden when the list has not loaded', () => {
    nav(undefined);
    expect(screen.queryByRole('combobox', { name: 'Pack' })).toBeNull();
  });

  it('shows the default pack, and switching remembers the choice and restarts the console', () => {
    nav(packs);
    const sel = screen.getByRole('combobox', { name: 'Pack' }) as HTMLSelectElement;
    expect(sel.value).toBe('gpu');
    fireEvent.change(sel, { target: { value: 'shop' } });
    expect(getPack()).toBe('shop');
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('shows the remembered pack', () => {
    setPack('shop');
    nav(packs);
    expect((screen.getByRole('combobox', { name: 'Pack' }) as HTMLSelectElement).value).toBe('shop');
  });
});

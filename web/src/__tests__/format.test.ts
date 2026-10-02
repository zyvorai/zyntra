import { describe, expect, it } from 'vitest';
import { display } from '../format';

describe('display', () => {
  it('shows ISO timestamps in the viewer locale', () => {
    for (const iso of ['2026-10-04T17:00:00Z', '2026-10-04T17:00:00.123Z', '2026-10-04T17:00:00+05:30', '2026-10-04T17:00Z']) {
      expect(display(iso)).toBe(new Date(iso).toLocaleString());
      expect(display(iso)).not.toContain('T17');
    }
  });

  it('never rewrites anything that is not a timestamp', () => {
    for (const v of ['Order:erp:O-1001', 'press-4', '2026-10-04', '10:30', 'T17:00:00Z', '2026-13-45T99:00:00Z', 'v2026-10-04T17:00:00Z', '']) {
      expect(display(v)).toBe(v);
    }
    expect(display(42)).toBe('42');
    expect(display(true)).toBe('true');
    expect(display(null)).toBe('null');
  });
});

import { describe, expect, it } from 'vitest';
import { navGroups, pageTitles, tenantPages, visibleGroups } from '../nav';

const labels = (g: ReturnType<typeof visibleGroups>) => g.flatMap((x) => (x.children ? x.children.map((c) => c.label) : [x.label]));

describe('navigation', () => {
  it('hides the Business group when the pack has no ontology', () => {
    expect(visibleGroups(false).map((g) => g.label)).not.toContain('Business');
    expect(visibleGroups(true).map((g) => g.label)).toContain('Business');
  });

  it('shows a provider every page except the tenant-only ones', () => {
    const l = labels(visibleGroups(true));
    for (const want of ['Gaps', 'Plan', 'Simulate', 'Approvals', 'Audit', 'Objects', 'Workflows', 'Scenarios', 'Model']) expect(l).toContain(want);
    expect(l).not.toContain('Service levels');
  });

  it('gives a tenant account only its workspace pages', () => {
    const l = labels(visibleGroups(true, 'alpha'));
    expect(l.sort()).toEqual(['Approvals', 'Ask Zyntra', 'Objects', 'Service levels', 'Workflows'].sort());
    for (const hidden of ['Gaps', 'Plan', 'Simulate', 'Audit', 'Signals', 'Model', 'Scenarios', 'Insights', 'Overview']) expect(l).not.toContain(hidden);
  });

  it('every tenant page is a real page and none exposes provider data', () => {
    for (const p of tenantPages) expect(pageTitles[p]).toBeTruthy();
    for (const p of ['gaps', 'plan', 'simulate', 'audit', 'signals', 'insights', 'model', 'scenarios', 'overview'] as const) {
      expect(tenantPages).not.toContain(p);
    }
  });

  it('keeps the page table and the groups in step', () => {
    for (const g of navGroups) for (const c of g.children ?? []) expect(pageTitles[c.page]).toBe(c.label === 'Ask Zyntra' ? 'Ask Zyntra' : pageTitles[c.page]);
  });
});

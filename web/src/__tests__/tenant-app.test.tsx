import { render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import App from '../App';
import { mockApi } from '../test/mock';

const meta = { product: 'Zyntra', version: 't', host: 'h', model: 'm', auth_required: true, sources: { total: 0, healthy: 0 }, approval_mode: 'local', execute_mode: 'dry-run', ai_mode: 'heuristic', ontology: true };

function who(tenant?: string, role = 'approver') {
  return { identity: { subject: 'ann', role, roles: [role], method: 'password', ...(tenant ? { tenant } : {}) }, auth_required: true, methods: ['password'] };
}

describe('console as a tenant account', () => {
  it('lands on service levels, never fetches provider data and ignores hash routes to hidden pages', async () => {
    window.location.hash = '#/gaps';
    const calls = mockApi({
      'GET /api/v1/meta': meta,
      'GET /api/v1/whoami': who('alpha'),
      'GET /api/v1/tenant/kpis': { tenant: 'alpha', kpis: [{ id: 'alpha_latency', name: 'Alpha API latency', unit: 'ms', value: 250, target: 200, direction: 'lower', met: false }] },
    });
    render(<App />);
    expect(await screen.findByText('Alpha API latency')).toBeTruthy();
    expect(screen.getByText('off target')).toBeTruthy();
    expect(screen.getByText(/workspace alpha/)).toBeTruthy();
    // No provider navigation.
    for (const hidden of ['Gaps', 'Plan', 'Simulate', 'Signals', 'Model', 'Insights']) {
      expect(screen.queryByRole('button', { name: new RegExp(`^${hidden}`) })).toBeNull();
    }
    // The page never asked the server for anything deployment-wide.
    const forbidden = calls.filter((c) => /\/api\/v1\/(gaps|plan|graph|sources|policy|audit|events|scenarios)/.test(c.path));
    expect(forbidden).toEqual([]);
  });

  it('shows a provider the full console', async () => {
    mockApi({
      'GET /api/v1/meta': meta,
      'GET /api/v1/whoami': who(undefined, 'admin'),
      'GET /api/v1/overview': {},
    });
    render(<App />);
    await waitFor(() => expect(screen.getAllByRole('button', { name: /^(Decide|Act|Business)/ }).length).toBeGreaterThan(0));
    expect(screen.queryByText(/workspace /)).toBeNull();
  });
});

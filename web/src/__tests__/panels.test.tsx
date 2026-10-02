import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { WhoAmI } from '../api';
import Decision from '../pages/Decision';
import Insights from '../pages/Insights';
import { WhoContext } from '../session';
import { mockApi } from '../test/mock';

const who = (role: 'admin' | 'viewer'): WhoAmI => ({ identity: { subject: 'u', role, roles: [role], method: 'password' }, auth_required: true });

const decision = {
  id: 'prop-1', action: 'add', action_name: 'Add GPUs', status: 'executed', phase: 'executed', created_at: '2026-10-02T00:00:00Z', created_by: 'p',
  approvals: [], required_approvals: 1, risk: 'low', baseline: {}, predicted: { severity_before: 1, severity_after: 0.4, closes: [], opens: [], kpis: {} }, rollout: { stages: [{ name: 'canary', sites: ['a'] }, { name: 'fleet', sites: ['b'] }] },
};
const rollout = (state: string, current: string | undefined, reason = '') => ({
  rollout: {
    id: 'prop-1', action: 'add', state, reason, current,
    stages: [
      { name: 'canary', sites: ['a'], state: state === 'halted' ? 'blocked' : 'healthy', reports: { a: { state: 'healthy', by: 'deploy', at: '2026-10-02T00:00:00Z' } },
        gate_check: [{ kpi: 'wait', max: 30, value: 55, ok: false, reason: '55 is above the limit 30' }] },
      { name: 'fleet', sites: ['b'], state: 'waiting' },
    ],
  },
  execute_mode: 'dry-run',
});

describe('Rollout panel on a decision', () => {
  const routes = (r: unknown) => ({
    'GET /api/v1/decisions/prop-1': { decision: decision, audit: null },
    'GET /api/v1/proposals/prop-1/explanation': { status: 409, body: { error: 'none' } },
    // The server sends null, not [], when there is no precedent. A fixture with [] hid a crash.
    'GET /api/v1/proposals/prop-1/similar': { items: null, text: 'No similar past decision on record.' },
    'GET /api/v1/rollouts/prop-1': r,
  });

  it('explains a halted rollout, the failed gate and offers recheck and abort to an admin', async () => {
    window.location.hash = '#/decision/prop-1';
    const calls = mockApi({ ...routes(rollout('halted', 'canary', 'stage canary: health gate on wait not met')), 'POST /api/v1/rollouts/prop-1/recheck': {}, 'POST /api/v1/rollouts/prop-1/abort': {} });
    render(<WhoContext.Provider value={who('admin')}><Decision /></WhoContext.Provider>);
    expect(await screen.findByText(/health gate on wait not met/)).toBeTruthy();
    expect(screen.getByText(/55 is above the limit 30/)).toBeTruthy();
    expect(screen.getByText('blocked')).toBeTruthy();
    expect(screen.getByText('waiting')).toBeTruthy();
    expect(screen.getByText(/may run now/)).toBeTruthy();
    expect(screen.getByText(/Execution is in dry-run mode/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Recheck gates' }));
    await waitFor(() => expect(calls.some((c) => c.path === '/api/v1/rollouts/prop-1/recheck')).toBe(true));

    const abort = screen.getByRole('button', { name: 'Abort rollout' }) as HTMLButtonElement;
    expect(abort.disabled).toBe(true); // a reason is required
    fireEvent.change(screen.getByLabelText('Reason to abort'), { target: { value: 'customer asked' } });
    fireEvent.click(abort);
    await waitFor(() => expect(calls.find((c) => c.path === '/api/v1/rollouts/prop-1/abort')?.body).toEqual({ reason: 'customer asked' }));
  });

  it('gives a viewer the status but no controls', async () => {
    window.location.hash = '#/decision/prop-1';
    mockApi(routes(rollout('open', 'fleet')));
    render(<WhoContext.Provider value={who('viewer')}><Decision /></WhoContext.Provider>);
    expect(await screen.findByText('fleet')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Abort rollout' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Recheck gates' })).toBeNull();
  });
});

describe('Model calibration card', () => {
  const insights = { 'GET /api/v1/ai/insights': { anomalies: [], forecasts: [] }, 'GET /api/v1/ai/status': { mode: 'heuristic', mutations: 'none' } };

  it('explains why a dry-run deployment has nothing to learn from', async () => {
    mockApi({ ...insights, 'GET /api/v1/ai/calibration': { decisions: 0, kpis: [], suggestions: [], note: 'No applied decision has a finished outcome yet. in dry-run mode nothing runs' } });
    render(<Insights setPage={() => {}} />);
    expect(await screen.findByText(/in dry-run mode nothing runs/)).toBeTruthy();
    expect(screen.getByText('0 decision(s)')).toBeTruthy();
  });

  it('shows the backtest and a suggested edit without applying anything', async () => {
    const calls = mockApi({
      ...insights,
      'GET /api/v1/ai/calibration': {
        decisions: 9,
        kpis: [{ kpi: 'wait', n: 9, mean_abs_error: 0.12, hit_rate: 0.78, bias: -0.05 }],
        suggestions: [{ kpi: 'wait', n: 9, edges: [], loo_error_before: 0.1, loo_error_after: 0.04, improvement: 0.6, yaml: '  - { from: queue, to: wait, weight: 0.9 } # was 0.5', why: 'Across 9 decisions the edges into wait moved it about 1.8x as much as predicted.' }],
        notes: ['gpus: 2 decision(s) moved it through an edge; 5 are needed'],
      },
    });
    render(<Insights setPage={() => {}} />);
    expect(await screen.findByText(/moved it about 1.8x as much as predicted/)).toBeTruthy();
    expect(screen.getByText(/weight: 0.9/)).toBeTruthy();
    expect(screen.getByText(/Suggestions are never applied automatically/)).toBeTruthy();
    expect(screen.getByText(/5 are needed/)).toBeTruthy();
    expect(calls.filter((c) => c.method !== 'GET')).toEqual([]); // read-only
  });

  it('shows a suggested action effect, and an older server without the field still renders', async () => {
    mockApi({
      ...insights,
      'GET /api/v1/ai/calibration': {
        decisions: 8,
        kpis: [],
        suggestions: [],
        action_suggestions: [{ action: 'restart', kpi: 'wait', n: 8, declared: -0.2, scale: 1.5, suggested: -0.3, loo_error_before: 0.1, loo_error_after: 0.02, improvement: 0.8, yaml: '  - { kpi: wait, change: -0.3 } # was -0.2', why: 'Across 8 decisions that ran only restart, wait moved about 1.5x as much as the declared effect.' }],
      },
    });
    const { unmount } = render(<Insights setPage={() => {}} />);
    expect(await screen.findByText(/ran only restart/)).toBeTruthy();
    expect(screen.getByText(/change: -0.3/)).toBeTruthy();
    unmount();
    mockApi({ ...insights, 'GET /api/v1/ai/calibration': { decisions: 1, kpis: [], suggestions: [] } });
    render(<Insights setPage={() => {}} />);
    expect(await screen.findByText('1 decision(s)')).toBeTruthy();
  });
});

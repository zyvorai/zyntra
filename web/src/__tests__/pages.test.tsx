import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { WhoAmI } from '../api';
import Objects from '../pages/Objects';
import Scenarios from '../pages/Scenarios';
import ServiceLevels from '../pages/ServiceLevels';
import Workflows from '../pages/Workflows';
import { WhoContext } from '../session';
import { mockApi } from '../test/mock';

const prov = { source: 'netra', source_id: 'row:1', observed_at: '2026-10-02T00:00:00Z', ingested_at: '2026-10-02T00:01:00Z' };
const obj = (id: string, type: string, name: string, extra: Record<string, unknown> = {}) => ({
  id,
  type,
  props: { name: { v: name, prov }, ...Object.fromEntries(Object.entries(extra).map(([k, v]) => [k, { v, prov }])) },
});
const admin: WhoAmI = { identity: { subject: 'root', role: 'admin', roles: ['admin'], method: 'password' }, auth_required: true };
const viewer: WhoAmI = { identity: { subject: 'v', role: 'viewer', roles: ['viewer'], method: 'password' }, auth_required: true };
const withWho = (w: WhoAmI, ui: React.ReactNode) => <WhoContext.Provider value={w}>{ui}</WhoContext.Provider>;

const schema = {
  objects: [{ name: 'Cluster', properties: [] }, { name: 'Service', properties: [] }],
  links: [],
  actions: [{ id: 'raise', inputs: [{ name: 'service', object_type: 'Service', required: true }] }],
  views: [{ id: 'services', title: 'Services', type: 'Service', columns: ['name', 'tier'], actions: ['raise'], exposed: true }],
};

describe('Objects page', () => {
  const base = {
    'GET /api/v1/ontology/schema': schema,
    'GET /api/v1/ontology/objects': { objects: [obj('Cluster:x:a', 'Cluster', 'Alpha Cluster'), obj('Service:x:s', 'Service', 'Inference API')] },
    'GET /api/v1/ontology/resolution': { candidates: [] },
  };

  it('lists objects and shows provenance, links and change history for the selection', async () => {
    window.location.hash = '#/objects/Cluster%3Ax%3Aa';
    mockApi({
      ...base,
      'GET /api/v1/ontology/connectors': { status: 403, body: { error: 'insufficient role' } },
      'GET /api/v1/ontology/objects/Cluster:x:a': {
        object: obj('Cluster:x:a', 'Cluster', 'Alpha Cluster', { gpus: 8 }),
        links: [{ link: { id: 'l1', type: 'runs_on', from: 'Service:x:s', to: 'Cluster:x:a', prov }, other: obj('Service:x:s', 'Service', 'Inference API'), out: false }],
        impact: [{ object: obj('Service:x:s', 'Service', 'Inference API'), depth: 1, via: 'runs_on' }],
        bound_kpis: ['gpu_utilization'],
        failing_kpis: ['gpu_utilization'],
      },
      'GET /api/v1/ontology/objects/Cluster:x:a/history': {
        changes: [{ object: 'Cluster:x:a', property: 'gpus', before: { v: 4, prov }, after: { v: 8, prov } }],
      },
    });
    render(withWho(viewer, <Objects />));
    expect((await screen.findAllByText('Alpha Cluster')).length).toBeGreaterThan(0);
    expect(await screen.findByText('failing gpu_utilization')).toBeTruthy();
    expect(screen.getAllByText('netra').length).toBeGreaterThan(0); // provenance source
    expect(screen.getByText(/Change history \(1\)/)).toBeTruthy();
    expect(screen.getByText(/Reachability through links/)).toBeTruthy();
    // A viewer gets no Sources card: the endpoint refused it.
    expect(screen.queryByText('Sources')).toBeNull();
  });

  it('shows connector health and lets only an admin run one', async () => {
    const statuses = [
      { name: 'k8s-nodes', kind: 'kubernetes', interval: '1m0s', last_success: new Date().toISOString(), last_objects: 12, last_links: 0, last_skipped: 0, duration_ms: 5, runs: 3, failures: 0, streak: 0, running: false, healthy: true },
      { name: 'crm', kind: 'sql', interval: '5m0s', last_error: 'connection refused', last_objects: 0, last_links: 0, last_skipped: 0, duration_ms: 1, runs: 4, failures: 4, streak: 4, running: false, healthy: false },
    ];
    const calls = mockApi({ ...base, 'GET /api/v1/ontology/connectors': { connectors: statuses }, 'POST /api/v1/ontology/connectors/crm/run': {} });
    const { unmount } = render(withWho(admin, <Objects />));
    expect(await screen.findByText('1 of 2 healthy')).toBeTruthy();
    expect(screen.getByText('connection refused')).toBeTruthy();
    expect(screen.getByText(/retrying less often \(4 failures\)/)).toBeTruthy();
    const row = screen.getByText('crm').closest('tr') as HTMLElement;
    fireEvent.click(within(row).getByRole('button', { name: 'Run now' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path === '/api/v1/ontology/connectors/crm/run')).toBe(true));
    unmount();

    render(withWho(approverOnly(), <Objects />));
    await screen.findByText('1 of 2 healthy');
    expect(screen.queryByRole('button', { name: 'Run now' })).toBeNull();
  });

  it('lets an approver decide an identity match', async () => {
    const calls = mockApi({
      ...base,
      'GET /api/v1/ontology/resolution': { candidates: [{ id: 'c1', a: 'Machine:erp:M1', b: 'Machine:mes:44', reason: 'names normalise to the same value', status: 'pending' }] },
      'GET /api/v1/ontology/connectors': { status: 403, body: { error: 'x' } },
      'POST /api/v1/ontology/resolution/c1/accept': {},
    });
    render(withWho(approverOnly(), <Objects />));
    fireEvent.click(await screen.findByRole('button', { name: /merge/i }));
    await waitFor(() => expect(calls.some((c) => c.path === '/api/v1/ontology/resolution/c1/accept')).toBe(true));
  });
});

function approverOnly(): WhoAmI {
  return { identity: { subject: 'boss', role: 'approver', roles: ['approver'], method: 'password' }, auth_required: true };
}

describe('Workflows page', () => {
  it('flags exposed objects and proposes the typed action with the object as input', async () => {
    const calls = mockApi({
      'GET /api/v1/ontology/schema': schema,
      'GET /api/v1/ontology/views/services': {
        view: schema.views[0],
        rows: [
          { id: 'Service:x:s', cells: { name: 'Inference API', tier: 'inference' }, failing_kpis: ['queue'], exposed_by: ['Cluster A'] },
          { id: 'Service:x:t', cells: { name: 'Batch', tier: 'batch' } },
        ],
      },
      'POST /api/v1/proposals': { id: 'prop-1' },
    });
    render(withWho(admin, <Workflows />));
    expect(await screen.findByText('failing queue')).toBeTruthy();
    expect(screen.getByText('exposed via Cluster A')).toBeTruthy();
    expect(screen.getByText('clear')).toBeTruthy();
    fireEvent.click(screen.getAllByRole('button', { name: 'Propose raise' })[0]);
    await waitFor(() => expect(screen.getByText(/Proposal prop-1 created/)).toBeTruthy());
    const post = calls.find((c) => c.method === 'POST' && c.path === '/api/v1/proposals');
    expect(post?.body).toEqual({ action: 'raise', inputs: { service: 'Service:x:s' } });
  });

  it('shows the reason the server gave when a proposal is refused', async () => {
    mockApi({
      'GET /api/v1/ontology/schema': schema,
      'GET /api/v1/ontology/views/services': { view: schema.views[0], rows: [{ id: 'Service:x:s', cells: { name: 'Inference API', tier: 'batch' } }] },
      'POST /api/v1/proposals': { status: 422, body: { error: 'action contract not met' } },
    });
    render(withWho(admin, <Workflows />));
    fireEvent.click(await screen.findByRole('button', { name: 'Propose raise' }));
    expect(await screen.findByText(/action contract not met/)).toBeTruthy();
  });
});

describe('Scenarios page', () => {
  const run = (id: string, name: string, actions: string[], before: number, after: number) => ({
    id, name, actions, created_by: 'p', created_at: '2026-10-02T00:00:00Z', ran_at: '2026-10-02T00:00:00Z', model_version: 'm1', data_version: 'd'.repeat(20),
    result: {
      kpis: [], weighted_before: 1, weighted_after: 0, gaps_closed: after === 0 ? ['queue'] : [], gaps_opened: [], at_risk_before: new Array(before).fill({ id: 'x', type: 'T', kpis: [] }), at_risk_after: new Array(after).fill({ id: 'x', type: 'T', kpis: [] }), exposed_before: [], exposed_after: [],
    },
  });

  it('saves a plan with assumptions and compares two scenarios', async () => {
    const calls = mockApi({
      'GET /api/v1/scenarios': { scenarios: [run('a', 'Add GPUs', ['add_gpus'], 2, 0), run('b', 'Do nothing', ['free'], 2, 2)] },
      'GET /api/v1/graph': { model: { actions: [{ id: 'add_gpus', name: 'Add GPUs' }, { id: 'free', name: 'Free' }] } },
      'POST /api/v1/scenarios': {},
      'GET /api/v1/scenarios/compare': {
        scenarios: ['a', 'b'],
        rows: [{ kpi: 'queue', name: 'Queue', before: 20, after: [8, 19], met: [true, false] }],
        weighted_after: [0, 0.9],
        objects_at_risk_after: [0, 2],
        objects_exposed_after: [0, 1],
      },
    });
    render(withWho(admin, <Scenarios />));
    fireEvent.change(await screen.findByLabelText('Scenario name'), { target: { value: 'Big plan' } });
    fireEvent.change(screen.getByLabelText('Assumptions'), { target: { value: 'queue=30' } });
    fireEvent.click(screen.getByLabelText('Add GPUs', { selector: 'input[type=checkbox]' }) as HTMLElement);
    fireEvent.click(screen.getByRole('button', { name: 'Save and run' }));
    await waitFor(() => expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ name: 'Big plan', actions: ['add_gpus'], assumptions: { queue: 30 } }));

    fireEvent.click(await screen.findByLabelText('Compare Add GPUs'));
    fireEvent.click(screen.getByLabelText('Compare Do nothing'));
    const table = await screen.findByText('Objects at risk after');
    const row = table.closest('tr') as HTMLElement;
    expect(within(row).getByText('2')).toBeTruthy();
    expect(screen.getByText('miss')).toBeTruthy();
  });

  it('rejects a malformed assumption before calling the server', async () => {
    const calls = mockApi({ 'GET /api/v1/scenarios': { scenarios: [] }, 'GET /api/v1/graph': { model: { actions: [{ id: 'a', name: 'A' }] } } });
    render(withWho(admin, <Scenarios />));
    fireEvent.change(await screen.findByLabelText('Scenario name'), { target: { value: 'x' } });
    fireEvent.change(screen.getByLabelText('Assumptions'), { target: { value: 'queue' } });
    fireEvent.click(screen.getByLabelText('A', { selector: 'input[type=checkbox]' }) as HTMLElement);
    fireEvent.click(screen.getByRole('button', { name: 'Save and run' }));
    expect(await screen.findByText(/must look like kpi=value/)).toBeTruthy();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
  });
});

describe('Service levels page', () => {
  it('marks each service level on target, off target or stale', async () => {
    mockApi({
      'GET /api/v1/tenant/kpis': {
        tenant: 'alpha',
        kpis: [
          { id: 'a', name: 'API latency', unit: 'ms', value: 250, target: 200, direction: 'lower', met: false },
          { id: 'b', name: 'Availability', unit: '%', value: 99.99, target: 99.9, direction: 'higher', met: true },
          { id: 'c', name: 'Throughput', value: 5, met: true, stale: true },
        ],
      },
    });
    render(<ServiceLevels />);
    expect(await screen.findByText('off target')).toBeTruthy();
    expect(screen.getByText('on target')).toBeTruthy();
    expect(screen.getByText('stale')).toBeTruthy();
    expect(screen.getByText(/≤ 200 ms/)).toBeTruthy();
  });

  it('says so when nothing is defined', async () => {
    mockApi({ 'GET /api/v1/tenant/kpis': { tenant: 'alpha', kpis: [] } });
    render(<ServiceLevels />);
    expect(await screen.findByText(/No service levels are defined/)).toBeTruthy();
  });
});

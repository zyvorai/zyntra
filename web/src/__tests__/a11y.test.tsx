import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { WhoAmI } from '../api';
import Ask from '../pages/Ask';
import Audit from '../pages/Audit';
import { WhoContext } from '../session';
import { mockApi } from '../test/mock';
import { accessibleName, unnamedControls } from '../test/a11y';

const admin: WhoAmI = { identity: { subject: 'root', role: 'admin', roles: ['admin'], method: 'password' }, auth_required: true };

describe('accessible names', () => {
  it('the helper treats a placeholder as no name', () => {
    document.body.innerHTML = '<input placeholder="Search"><input aria-label="Search"><button></button><button>Go</button>';
    const [bare, labelled, emptyBtn, btn] = Array.from(document.querySelectorAll('input, button'));
    expect(accessibleName(bare)).toBe('');
    expect(accessibleName(labelled)).toBe('Search');
    expect(accessibleName(emptyBtn)).toBe('');
    expect(accessibleName(btn)).toBe('Go');
  });

  it('Audit draws no control for events that belong to no decision', async () => {
    const ev = (n: number, proposal: string) => ({ seq: n, at: '2026-10-02T10:00:00Z', proposal, action: proposal ? 'add_gpus' : 'ontology:pack-files', from: '', to: proposal ? 'approved' : 'recorded', by: 'zyntra', note: 'x' });
    mockApi({
      'GET /api/v1/audit': { events: [ev(1, ''), ev(2, 'prop-1')], chain: { ok: true, events: 2, head: 'abc' } },
      'GET /api/v1/keep/audit': { status: 404, body: { error: 'none' } },
      'GET /api/v1/audit/verify': { chain: { ok: true, events: 2, head: 'abc' }, signing_key: 'k' },
    });
    const { container } = render(<WhoContext.Provider value={admin}><Audit /></WhoContext.Provider>);
    expect(await screen.findByRole('button', { name: 'prop-1' })).toBeTruthy();
    // One link for the decision event; none for the ingest note.
    expect(container.querySelectorAll('button.linklike').length).toBe(1);
    expect(unnamedControls(container)).toEqual([]);
  });

  it('every field on Ask has a name, not just a placeholder', async () => {
    mockApi({
      'GET /api/v1/ai/status': { mode: 'heuristic', mutations: 'never' },
      'GET /api/v1/ontology/actions': { actions: [{ id: 'raise', inputs: [{ name: 'service', object_type: 'Service', required: true }] }] },
    });
    const { container } = render(<WhoContext.Provider value={admin}><Ask /></WhoContext.Provider>);
    expect(await screen.findByLabelText('Describe the action and the objects')).toBeTruthy();
    expect(screen.getByLabelText('Your question')).toBeTruthy();
    expect(unnamedControls(container)).toEqual([]);
  });
});

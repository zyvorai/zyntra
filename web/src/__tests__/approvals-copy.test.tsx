import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { WhoAmI } from '../api';
import Approvals from '../pages/Approvals';
import { WhoContext } from '../session';
import { mockApi } from '../test/mock';

const admin: WhoAmI = { identity: { subject: 'root', role: 'admin', roles: ['admin'], method: 'password' }, auth_required: true };

// The page used to say every approved action "runs as a Gravia CRD" and that
// "kubectl validates against the API server", which is only true for the gpu
// pack. A manufacturing or logistics pack records tasks and writes files.
describe('Approvals copy', () => {
  it('does not claim every action is a kubectl or Gravia change', async () => {
    mockApi({ 'GET /api/v1/proposals': { proposals: [], approval_mode: 'local', execute_mode: 'dry-run', double_approval: false } });
    render(<WhoContext.Provider value={admin}><Approvals /></WhoContext.Provider>);
    expect(await screen.findByText(/nothing is changed/)).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/Gravia/);
    expect(document.body.textContent).toMatch(/advisory action records only the decision/);
  });
});

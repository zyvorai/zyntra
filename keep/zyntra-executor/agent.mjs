// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
//
// zyntra-executor runs inside a Fabric Keep sandbox. It never holds a secret:
// the zyntra-exec credential is injected by Keep's broker, and because the
// credential lists POST under requires_approval, every call is held for a
// Keep approval and receipted.
//
// input.mode:
//   "execute" — POST input.url (Zyntra /api/v1/exec/{id}) and return the result.
//   "record"  — wait for the Keep approval Zyntra raises on this session to be
//               decided, so the decision lands in Keep's audit chain.

export default {
  async run(ctx) {
    const input = ctx.input || {};
    const id = String(input.proposal_id || '');
    if (!/^[a-z0-9-]{1,64}$/.test(id)) throw new Error('input.proposal_id is required');

    if (input.mode === 'record') {
      ctx.emit('zyntra.record', { proposal_id: id, action: input.action, status: input.status, by: input.by });
      const steer = await ctx.nextSteer({ timeoutMs: 120000 });
      return { recorded: steer !== null, proposal_id: id, decision: steer ? steer.decision ?? null : null };
    }

    const url = String(input.url || '');
    if (!/^https:\/\/127\.0\.0\.1:\d+\/api\/v1\/exec\/[a-z0-9-]+$/.test(url) || !url.endsWith(`/${id}`)) {
      throw new Error('input.url must be the Zyntra exec URL for this proposal');
    }
    ctx.emit('zyntra.execute', { proposal_id: id, action: input.action });
    const res = await ctx.fetch(url, {
      method: 'POST',
      credential: 'zyntra-exec',
      headers: { 'content-type': 'application/json', 'idempotency-key': `zyntra-${id}` },
      body: JSON.stringify({ proposal_id: id }),
    });
    const text = await res.text();
    let body;
    try {
      body = JSON.parse(text);
    } catch {
      body = { raw: text.slice(0, 2000) };
    }
    ctx.emit('zyntra.executed', { proposal_id: id, http_status: res.status, status: body.status });
    if (!res.ok) throw new Error(`zyntra exec answered HTTP ${res.status}: ${body.error || text.slice(0, 300)}`);
    return { proposal_id: id, status: body.status, ok: body.execution ? body.execution.ok : null };
  },
};

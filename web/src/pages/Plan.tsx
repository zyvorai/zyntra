import { useState } from 'react';
import { api, can, sev, type Answer, type PlanResponse, type Proposal, type Recommendation } from '../api';
import { useApi } from '../hooks';
import type { Page } from '../nav';
import { useWho } from '../session';
import SimResultView from '../components/SimResultView';
import { Card, Empty, ErrorNote, PageHero, Pill, riskTone } from '../components/ui';

const confTone = { high: 'ok', medium: 'warn', low: 'bad' } as const;

export default function Plan({ setPage }: { setPage: (p: Page) => void }) {
  const { data, error } = useApi<PlanResponse>('/api/v1/plan', 15000);
  const [open, setOpen] = useState<string | null>(null);
  const [explain, setExplain] = useState<Record<string, Answer>>({});
  const [busy, setBusy] = useState('');
  const [note, setNote] = useState('');
  const who = useWho();
  const mayPropose = can(who, 'propose');
  const recs = data?.recommendations ?? [];
  const blocked = data?.blocked ?? [];

  const doExplain = async (action: string) => {
    setBusy(`x-${action}`);
    try {
      const a = await api<Answer>('/api/v1/ai/explain', { method: 'POST', json: { action: action.split('+')[0] } });
      setExplain((e) => ({ ...e, [action]: a }));
    } catch (e) {
      setNote((e as Error).message);
    } finally {
      setBusy('');
    }
  };

  const propose = async (action: string) => {
    setBusy(`p-${action}`);
    setNote('');
    try {
      const p = await api<Proposal>('/api/v1/proposals', { method: 'POST', json: { action } });
      setNote(`Proposal ${p.id} for “${p.action_name}” is ${p.status}; it needs ${p.required_approvals || 1} approval(s).`);
    } catch (e) {
      setNote((e as Error).message);
    } finally {
      setBusy('');
    }
  };

  const band = (r: Recommendation) => {
    const best = r.result.weighted_after_best;
    const worst = r.result.weighted_after_worst;
    if (best === undefined || worst === undefined || Math.abs(worst - best) < 1e-9) return null;
    return (
      <span className="band">
        (severity after {sev(best)}–{sev(worst)})
      </span>
    );
  };

  return (
    <>
      <PageHero
        eyebrow="Decide"
        title="Plan"
        tint="green"
        lede="Every action, and pairs that work together, simulated against live values. Ranked by criticality-weighted improvement minus risk, uncertainty and stale-input penalties. Anything that would breach a hard constraint is listed separately. Nothing runs until a human approves it."
      />
      <ErrorNote message={error} />
      {data?.unusable_inputs?.length ? (
        <p className="info-note">
          Stale or missing inputs: <strong>{data.unusable_inputs.join(', ')}</strong>. Predictions that depend on them are penalised and marked low confidence.
        </p>
      ) : null}
      {note ? (
        <p className="info-note">
          {note}{' '}
          <button className="linklike" onClick={() => setPage('approvals')}>
            Open approvals
          </button>
        </p>
      ) : null}
      {data && recs.length === 0 ? <Empty>No action improves the current gaps without breaking a constraint.</Empty> : null}
      <div className="stack">
        {recs.map((r) => (
          <Card
            key={r.action}
            title={
              <>
                <span className="rank">{r.rank}</span> {r.name}
              </>
            }
            aside={
              <div className="pills">
                {r.actions && r.actions.length > 1 ? <Pill tone="purple">combined</Pill> : null}
                {r.adapter ? <Pill tone="info">{r.adapter}</Pill> : null}
                <Pill tone={riskTone(r.risk)}>{r.risk || 'low'} risk</Pill>
                <Pill tone={confTone[r.confidence] ?? 'neutral'}>{r.confidence} confidence</Pill>
              </div>
            }
          >
            <div className="rec-metrics">
              <span>
                Weighted improvement <strong>{r.weighted_improvement.toFixed(3)}</strong> {band(r)}
              </span>
              <span>
                Score <strong>{r.score.toFixed(3)}</strong>
              </span>
              <span>
                Closes <strong>{r.result.gaps_closed?.length ?? 0}</strong>
              </span>
              {r.result.gaps_opened?.length ? (
                <span className="down">
                  Opens <strong>{r.result.gaps_opened.length}</strong>
                </span>
              ) : null}
              {r.settles_after ? (
                <span>
                  Settles in <strong>{r.settles_after}</strong>
                </span>
              ) : null}
              {r.stale_inputs?.length ? <span className="down">stale: {r.stale_inputs.join(', ')}</span> : null}
            </div>
            {explain[r.action] ? <p className="prose ai-text">{explain[r.action].text}</p> : null}
            {open === r.action ? <SimResultView r={r.result} /> : null}
            <div className="row-actions">
              {mayPropose ? (
                <button className="primary" disabled={busy !== ''} onClick={() => propose(r.action)}>
                  {busy === `p-${r.action}` ? 'Proposing…' : 'Propose for approval'}
                </button>
              ) : null}
              <button className="btn-diag" disabled={busy !== ''} onClick={() => doExplain(r.action)}>
                {busy === `x-${r.action}` ? 'Explaining…' : 'Explain'}
              </button>
              <button className="btn-secondary" onClick={() => setOpen(open === r.action ? null : r.action)}>
                {open === r.action ? 'Hide impact' : 'Show impact'}
              </button>
            </div>
          </Card>
        ))}
      </div>

      {blocked.length ? (
        <Card title="Blocked by hard constraints" aside={<Pill tone="bad">{blocked.length}</Pill>}>
          <p className="muted small">These would improve some KPIs but break a constraint, so Zyntra will not propose them.</p>
          <table className="table compact">
            <thead>
              <tr>
                <th>Action</th>
                <th className="num">Weighted improvement</th>
                <th>Why blocked</th>
              </tr>
            </thead>
            <tbody>
              {blocked.map((b) => (
                <tr key={b.action}>
                  <td>{b.name}</td>
                  <td className="num">{b.weighted_improvement.toFixed(3)}</td>
                  <td className="down small">{(b.blocked_reasons ?? []).join('; ')}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      ) : null}
    </>
  );
}

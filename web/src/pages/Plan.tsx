import { useState } from 'react';
import { api, pct, type Answer, type Proposal, type Recommendation } from '../api';
import { useApi } from '../hooks';
import type { Page } from '../nav';
import SimResultView from '../components/SimResultView';
import { Card, Empty, ErrorNote, PageHero, Pill, riskTone } from '../components/ui';

export default function Plan({ setPage }: { setPage: (p: Page) => void }) {
  const { data, error } = useApi<{ recommendations: Recommendation[] }>('/api/v1/plan', 15000);
  const [open, setOpen] = useState<string | null>(null);
  const [explain, setExplain] = useState<Record<string, Answer>>({});
  const [busy, setBusy] = useState('');
  const [note, setNote] = useState('');
  const recs = data?.recommendations ?? [];

  const doExplain = async (action: string) => {
    setBusy(`x-${action}`);
    try {
      const a = await api<Answer>('/api/v1/ai/explain', { method: 'POST', json: { action } });
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
      setNote(`Proposal ${p.id} for “${p.action_name}” is ${p.status}.`);
    } catch (e) {
      setNote((e as Error).message);
    } finally {
      setBusy('');
    }
  };

  return (
    <>
      <PageHero
        eyebrow="Decide"
        title="Plan"
        tint="green"
        lede="Every action in the model, simulated against live values and ranked by improvement minus a risk penalty. Nothing runs until a human approves it."
      />
      <ErrorNote message={error} />
      {note ? (
        <p className="info-note">
          {note}{' '}
          <button className="linklike" onClick={() => setPage('approvals')}>
            Open approvals
          </button>
        </p>
      ) : null}
      {data && recs.length === 0 ? <Empty>No actions in the model improve the current gaps.</Empty> : null}
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
                {r.adapter ? <Pill tone="info">{r.adapter}</Pill> : null}
                <Pill tone={riskTone(r.risk)}>{r.risk || 'low'} risk</Pill>
              </div>
            }
          >
            <div className="rec-metrics">
              <span>
                Improvement <strong>{pct(r.improvement)}</strong>
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
            </div>
            {explain[r.action] ? <p className="prose ai-text">{explain[r.action].text}</p> : null}
            {open === r.action ? <SimResultView r={r.result} /> : null}
            <div className="row-actions">
              <button className="primary" disabled={busy !== ''} onClick={() => propose(r.action)}>
                {busy === `p-${r.action}` ? 'Proposing…' : 'Propose for approval'}
              </button>
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
    </>
  );
}

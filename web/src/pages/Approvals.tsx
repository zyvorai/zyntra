import { useState } from 'react';
import { ShieldCheck } from 'lucide-react';
import { ago, api, fmt, sev, type Proposal, type ProposalStatus } from '../api';
import { useApi } from '../hooks';
import { Card, Empty, ErrorNote, PageHero, Pill, riskTone } from '../components/ui';

interface ProposalList {
  proposals: Proposal[];
  approval_mode: string;
  execute_mode: string;
  double_approval: boolean;
}

const statusTone: Record<ProposalStatus, 'info' | 'ok' | 'bad' | 'warn' | 'neutral'> = {
  pending: 'info',
  approved: 'warn',
  rejected: 'neutral',
  executed: 'ok',
  failed: 'bad',
};

const filters: Array<ProposalStatus | 'all'> = ['pending', 'approved', 'executed', 'failed', 'rejected', 'all'];

export default function Approvals() {
  const { data, error, reload } = useApi<ProposalList>('/api/v1/proposals', 5000);
  const [filter, setFilter] = useState<ProposalStatus | 'all'>('pending');
  const [reason, setReason] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState('');
  const [note, setNote] = useState('');

  const all = data?.proposals ?? [];
  const shown = filter === 'all' ? all : all.filter((p) => p.status === filter);
  const keepMode = data?.approval_mode === 'keep';

  const decide = async (p: Proposal, approve: boolean) => {
    if (approve && data?.execute_mode === 'apply' && !window.confirm(`Apply “${p.action_name}” to the cluster?`)) return;
    setBusy(p.id);
    setNote('');
    try {
      const out = await api<Proposal>(`/api/v1/proposals/${p.id}/${approve ? 'approve' : 'reject'}`, {
        method: 'POST',
        json: { reason: reason[p.id] || '' },
      });
      setNote(
        out.status === 'approved' && out.keep?.session_id
          ? `Approved. Fabric Keep session ${out.keep.session_id} is running the executor${data?.double_approval ? ' — a second approval is waiting in Keep.' : '.'}`
          : `Proposal ${out.id} is now ${out.status}.`
      );
      reload();
    } catch (e) {
      setNote((e as Error).message);
    } finally {
      setBusy('');
    }
  };

  return (
    <>
      <PageHero
        eyebrow="Act"
        title="Approvals"
        lede={
          <>
            Proposals wait here for a human decision. Approved actions run as Gravia CRDs in{' '}
            <strong>{data?.execute_mode ?? '…'}</strong> mode
            {keepMode ? ', executed by a signed Fabric Keep agent through the Keep broker.' : '.'}
          </>
        }
      />
      <ErrorNote message={error} />
      {data ? (
        <div className="banner">
          <ShieldCheck size={18} aria-hidden />
          <span>
            {keepMode ? (
              <>
                <strong>Keep-gated.</strong> Approving opens a Keep session; the executor token is injected by Keep's broker and every call is
                receipted in Keep's audit chain.
              </>
            ) : (
              <>
                <strong>Local approvals.</strong> Decisions are executed by Zyntra and mirrored into Fabric Keep's audit when it is reachable.
              </>
            )}{' '}
            {data.execute_mode === 'dry-run' ? (
              <>
                Execution is <strong>dry-run</strong>: kubectl validates against the API server without persisting.
              </>
            ) : (
              <>
                Execution is <strong>live</strong>.
              </>
            )}
          </span>
        </div>
      ) : null}
      {note ? <p className="info-note">{note}</p> : null}

      <div className="segmented" role="tablist">
        {filters.map((f) => {
          const n = f === 'all' ? all.length : all.filter((p) => p.status === f).length;
          return (
            <button key={f} role="tab" aria-selected={filter === f} onClick={() => setFilter(f)}>
              {f} {n ? <span className="count">{n}</span> : null}
            </button>
          );
        })}
      </div>

      {data && shown.length === 0 ? (
        <Empty>{filter === 'pending' ? 'Inbox zero. Propose an action from the Plan page.' : `No ${filter} proposals.`}</Empty>
      ) : null}

      <div className="stack">
        {shown.map((p) => (
          <Card
            key={p.id}
            title={p.action_name}
            aside={
              <div className="pills">
                <Pill tone={riskTone(p.risk)}>{p.risk || 'low'} risk</Pill>
                <Pill tone={statusTone[p.status]}>{p.status}</Pill>
              </div>
            }
          >
            <p className="muted small">
              <span className="mono">{p.id}</span> · proposed by {p.created_by} {ago(p.created_at)}
              {p.decided_by ? ` · ${p.status === 'rejected' ? 'rejected' : 'approved'} by ${p.decided_by} ${ago(p.decided_at)}` : ''}
              {p.reason ? ` — “${p.reason}”` : ''}
            </p>

            <div className="rec-metrics">
              <span>
                Severity <strong>{sev(p.predicted.severity_before)}</strong> → <strong>{sev(p.predicted.severity_after)}</strong>
              </span>
              {p.predicted.closes?.map((g) => (
                <Pill key={g} tone="ok">
                  closes {g}
                </Pill>
              ))}
              {p.predicted.opens?.map((g) => (
                <Pill key={g} tone="bad">
                  opens {g}
                </Pill>
              ))}
            </div>

            {Object.keys(p.predicted.kpis).length ? (
              <table className="table compact">
                <thead>
                  <tr>
                    <th>KPI</th>
                    <th className="num">Baseline</th>
                    <th className="num">Predicted</th>
                    <th className="num">Actual</th>
                  </tr>
                </thead>
                <tbody>
                  {Object.entries(p.predicted.kpis).map(([k, v]) => (
                    <tr key={k}>
                      <td className="mono">{k}</td>
                      <td className="num">{fmt(p.baseline[k])}</td>
                      <td className="num">{fmt(v)}</td>
                      <td className="num">{p.actual && k in p.actual ? fmt(p.actual[k]) : <span className="muted">—</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : null}

            {p.render ? (
              <details className="trace" open={p.status === 'pending'}>
                <summary>Gravia change ({p.template})</summary>
                <pre className="code">{p.render}</pre>
              </details>
            ) : p.render_error ? (
              <p className="error-note">Cannot render {p.template}: {p.render_error}</p>
            ) : !p.template ? (
              <p className="muted small">Advisory action — no execute template; approving records the decision only.</p>
            ) : null}

            {p.execution ? (
              <details className="trace" open={!p.execution.ok}>
                <summary>
                  Execution · {p.execution.mode} · {p.execution.ok ? 'ok' : 'failed'} {p.executed_at ? ago(p.executed_at) : ''}
                </summary>
                <pre className="code">{[`$ ${p.execution.args.join(' ')}`, p.execution.output, p.execution.error].filter(Boolean).join('\n')}</pre>
              </details>
            ) : null}

            {p.keep ? (
              <p className="small keep-ref">
                <Pill tone={p.keep.error ? 'warn' : 'purple'}>Keep {p.keep.mode}</Pill>
                {p.keep.session_id ? <span className="mono">session {p.keep.session_id}</span> : null}
                {p.keep.approval_id ? <span className="mono">approval {p.keep.approval_id}</span> : null}
                {p.keep.receipt_id ? <span className="mono">receipt {p.keep.receipt_id}</span> : null}
                {p.keep.error ? <span className="down">{p.keep.error}</span> : null}
              </p>
            ) : null}

            {p.status === 'pending' ? (
              <div className="row-actions">
                <input
                  className="reason"
                  placeholder="Reason (optional, recorded in audit)"
                  value={reason[p.id] || ''}
                  onChange={(e) => setReason((r) => ({ ...r, [p.id]: e.target.value }))}
                  maxLength={500}
                />
                <button className="primary" disabled={busy !== '' || Boolean(p.render_error)} onClick={() => decide(p, true)}>
                  {busy === p.id ? 'Working…' : keepMode && p.template ? 'Approve via Keep' : 'Approve'}
                </button>
                <button className="btn-danger" disabled={busy !== ''} onClick={() => decide(p, false)}>
                  Reject
                </button>
              </div>
            ) : null}
          </Card>
        ))}
      </div>
    </>
  );
}

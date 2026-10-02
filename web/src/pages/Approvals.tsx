import { useState } from 'react';
import { ShieldCheck } from 'lucide-react';
import { ago, api, can, fmt, sev, until, renderLabel, type Precedents, type Proposal, type ProposalStatus } from '../api';
import { useApi } from '../hooks';
import { openDecision } from '../nav';
import { useWho } from '../session';
import { Card, Empty, ErrorNote, PageHero, Pill, riskTone } from '../components/ui';
import { phaseTone } from './Decision';

function PrecedentLine({ id }: { id: string }) {
  const r = useApi<Precedents>(`/api/v1/proposals/${encodeURIComponent(id)}/similar`, 0);
  if (!r.data?.items?.length) return null;
  return (
    <p className="small muted">
      <button className="linklike" onClick={() => openDecision(r.data!.items[0].proposal)}>
        Precedent
      </button>
      : {r.data.text}
    </p>
  );
}

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
  expired: 'neutral',
  blocked: 'bad',
  executed: 'ok',
  failed: 'bad',
};

const filters: Array<ProposalStatus | 'all'> = ['pending', 'approved', 'executed', 'blocked', 'failed', 'rejected', 'expired', 'all'];

export default function Approvals() {
  const { data, error, reload } = useApi<ProposalList>('/api/v1/proposals', 5000);
  const [filter, setFilter] = useState<ProposalStatus | 'all'>('pending');
  const [reason, setReason] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState('');
  const [note, setNote] = useState('');
  const who = useWho();
  const mayApprove = can(who, 'approve');

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
        out.status === 'pending' && approve
          ? `Approval recorded: ${out.approvals.length} of ${out.required_approvals} needed.`
          : out.status === 'approved' && out.keep?.session_id
            ? `Approved. Fabric Keep session ${out.keep.session_id} is running the executor${data?.double_approval ? ' — a second approval is waiting in Keep.' : '.'}`
            : out.status === 'approved' && out.waiting_for_window
              ? `Approved. It will run when its maintenance window opens.`
              : out.status === 'blocked'
                ? `Blocked: ${(out.blocked_reasons ?? []).join('; ')}`
                : `Proposal ${out.id} is now ${out.status} (${out.phase}).`
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
                {p.rollback_of ? <Pill tone="bad">rollback</Pill> : null}
                <Pill tone={riskTone(p.risk)}>{p.risk || 'low'} risk</Pill>
                <Pill tone={statusTone[p.status]}>{p.status}</Pill>
                {p.phase && p.phase !== p.status ? <Pill tone={phaseTone[p.phase] ?? 'neutral'}>{p.phase}</Pill> : null}
              </div>
            }
          >
            <p className="muted small">
              <span className="mono">{p.id}</span> · proposed by {p.created_by} {ago(p.created_at)}
              {p.decided_by ? ` · ${p.status === 'rejected' ? 'rejected' : 'approved'} by ${p.decided_by} ${ago(p.decided_at)}` : ''}
              {p.reason ? ` — “${p.reason}”` : ''} ·{' '}
              <button className="linklike" onClick={() => openDecision(p.id)}>
                decision record
              </button>
            </p>

            {p.status === 'pending' || p.approvals?.length ? (
              <div className="quorum">
                <span>
                  Approvals <strong>{p.approvals?.length ?? 0}</strong> of <strong>{Math.max(1, p.required_approvals || 1)}</strong>
                </span>
                {p.approvals?.map((a) => (
                  <Pill key={a.by} tone="ok">
                    {a.by}
                    {a.role ? ` · ${a.role}` : ''}
                  </Pill>
                ))}
                {p.policy?.distinct_from_proposer ? <span className="muted small">proposer cannot approve</span> : null}
                {p.expires_at && (p.status === 'pending' || p.status === 'approved') ? (
                  <Pill tone="warn">
                    {p.status === 'pending' ? 'proposal' : 'approval'} {until(p.expires_at)}
                  </Pill>
                ) : null}
                {p.waiting_for_window ? <Pill tone="info">waiting for {p.policy?.maintenance_windows?.join(', ')}</Pill> : null}
              </div>
            ) : null}

            {p.blocked_reasons?.length ? (
              <div className="error-note">
                <strong>Blocked before execution:</strong>
                <ul className="blocked-list">
                  {p.blocked_reasons.map((r) => (
                    <li key={r}>{r}</li>
                  ))}
                </ul>
              </div>
            ) : null}

            {p.outcome ? (
              <p className="small">
                <Pill tone={phaseTone[p.outcome.state] ?? 'neutral'}>outcome {p.outcome.state}</Pill>{' '}
                {p.outcome.state === 'observing'
                  ? `${p.outcome.samples.length} samples, watching until ${new Date(p.outcome.until).toLocaleTimeString()}`
                  : (p.outcome.reasons ?? []).join('; ')}
                {p.rollback_id ? (
                  <>
                    {' '}
                    ·{' '}
                    <button className="linklike" onClick={() => openDecision(p.rollback_id!)}>
                      rollback proposal
                    </button>
                  </>
                ) : null}
              </p>
            ) : null}

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
            <PrecedentLine id={p.id} />

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
                <summary>
                  {renderLabel(p.kinds, p.template)}
                  {p.template && p.kinds?.every((k) => k === 'kubectl') !== false ? ` (${p.template})` : ''}
                </summary>
                <pre className="code">{p.render}</pre>
              </details>
            ) : p.render_error ? (
              <p className="error-note">Cannot render {p.template || p.action}: {p.render_error}</p>
            ) : !p.template && !p.kinds?.length ? (
              <p className="muted small">Advisory action — nothing to run; approving records the decision only.</p>
            ) : null}
            {p.compensate?.length ? (
              <p className="muted small">
                Undo if it goes wrong: <span className="mono">{p.compensate.join(', ')}</span> (proposed for approval, never run on its own)
              </p>
            ) : null}

            {p.execution ? (
              <details className="trace" open={!p.execution.ok}>
                <summary>
                  Execution · {p.execution.mode} · {p.execution.ok ? 'ok' : 'failed'} {p.executed_at ? ago(p.executed_at) : ''}
                </summary>
                <pre className="code">
                  {[
                    p.execution.args?.length ? `$ ${p.execution.args.join(' ')}` : '',
                    p.execution.status ? `HTTP ${p.execution.status}` : '',
                    p.execution.response_hash ? `response sha256 ${p.execution.response_hash}` : '',
                    p.execution.written ? `wrote ${p.execution.written}` : '',
                    p.execution.output,
                    p.execution.error,
                  ]
                    .filter(Boolean)
                    .join('\n')}
                </pre>
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

            {p.status === 'pending' && mayApprove ? (
              <div className="row-actions">
                <input
                  className="reason"
                  placeholder="Reason (optional, recorded in audit)"
                  value={reason[p.id] || ''}
                  onChange={(e) => setReason((r) => ({ ...r, [p.id]: e.target.value }))}
                  maxLength={500}
                />
                <button
                  className="primary"
                  disabled={
                    busy !== '' ||
                    Boolean(p.render_error) ||
                    p.approvals?.some((a) => a.by === who?.identity.subject) ||
                    (p.policy?.distinct_from_proposer && p.created_by === who?.identity.subject)
                  }
                  onClick={() => decide(p, true)}
                >
                  {busy === p.id ? 'Working…' : keepMode && p.template ? 'Approve via Keep' : 'Approve'}
                </button>
                <button className="btn-danger" disabled={busy !== ''} onClick={() => decide(p, false)}>
                  Reject
                </button>
              </div>
            ) : p.status === 'pending' ? (
              <p className="muted small">Your role cannot approve proposals.</p>
            ) : null}
          </Card>
        ))}
      </div>
    </>
  );
}

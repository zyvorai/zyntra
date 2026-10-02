import { useEffect, useState } from 'react';
import { ArrowLeft, Download } from 'lucide-react';
import { ago, api, fmt, sev, type AuditEvent, type ExplanationResponse, type Phase, type Precedents, type Proposal, type Rollout } from '../api';
import { useApi } from '../hooks';
import { useWho } from '../session';
import { decisionFromHash, openDecision } from '../nav';
import SimResultView from '../components/SimResultView';
import { Card, Empty, ErrorNote, PageHero, Pill, riskTone } from '../components/ui';

interface DecisionResponse {
  decision: Proposal;
  audit: AuditEvent[];
  linked?: Proposal;
}

type Tone = 'ok' | 'warn' | 'bad' | 'info' | 'neutral' | 'purple';

export const phaseTone: Record<Phase, Tone> = {
  proposed: 'info',
  approved: 'warn',
  rejected: 'neutral',
  expired: 'neutral',
  blocked: 'bad',
  failed: 'bad',
  'dry-run-validated': 'ok',
  applied: 'info',
  observing: 'purple',
  verified: 'ok',
  regressed: 'bad',
  missed: 'warn',
  inconclusive: 'warn',
  'rollback-proposed': 'bad',
  'rolled-back': 'neutral',
};

function Step({ title, at, tone = 'neutral', children }: { title: string; at?: string; tone?: Tone; children?: React.ReactNode }) {
  return (
    <li className={`timeline-step tone-${tone}`}>
      <div className="timeline-head">
        <strong>{title}</strong>
        {at ? (
          <span className="muted small" title={at}>
            {ago(at)}
          </span>
        ) : null}
      </div>
      {children ? <div className="timeline-body">{children}</div> : null}
    </li>
  );
}

const stageTone = { waiting: 'neutral', running: 'info', healthy: 'ok', blocked: 'warn', failed: 'bad' } as const;

/** Staged delivery of an approved decision, as reported by deployment tooling. */
function RolloutPanel({ id }: { id: string }) {
  const { data, error, reload } = useApi<{ rollout: Rollout; execute_mode: string }>(`/api/v1/rollouts/${encodeURIComponent(id)}`, 10000);
  const who = useWho();
  const [err, setErr] = useState('');
  const [reason, setReason] = useState('');
  if (error || !data) return null;
  const ro = data.rollout;
  const canDecide = !!who && (who.identity.roles?.some((r) => r === 'approver' || r === 'admin') ?? false);
  const act = async (verb: 'recheck' | 'abort') => {
    setErr('');
    try {
      await api(`/api/v1/rollouts/${encodeURIComponent(id)}/${verb}`, { method: 'POST', json: verb === 'abort' ? { reason } : undefined });
      setReason('');
      reload();
    } catch (e) {
      setErr((e as Error).message);
    }
  };
  return (
    <Card title="Rollout" aside={<Pill tone={ro.state === 'complete' ? 'ok' : ro.state === 'open' ? 'info' : 'warn'}>{ro.state}</Pill>}>
      <p className="muted small">
        Zyntra does not deploy. Your deployment tooling reports each site; the next stage may start only when the previous one is healthy and its KPI gates hold.
        {data.execute_mode !== 'apply' ? ' Execution is in dry-run mode.' : ''}
      </p>
      {ro.reason ? <p className="error-note">{ro.reason}</p> : null}
      {err ? <p className="error-note">{err}</p> : null}
      <ol className="plain">
        {ro.stages.map((st) => (
          <li key={st.name}>
            <strong>{st.name}</strong> <Pill tone={stageTone[st.state]}>{st.state}</Pill>
            {ro.current === st.name ? <span className="muted small"> · may run now</span> : null}
            <div className="small">
              {st.sites.map((site) => (
                <span key={site} className="mono" style={{ marginRight: 12 }}>
                  {site}: {st.reports?.[site]?.state ?? '—'}
                </span>
              ))}
            </div>
            {st.gate_check?.map((g) => (
              <div key={g.kpi} className={g.ok ? 'muted small' : 'down small'}>
                gate {g.kpi} {g.max !== undefined ? `≤ ${g.max}` : ''} {g.min !== undefined ? `≥ ${g.min}` : ''}: {g.ok ? `ok (${g.value})` : g.reason}
              </div>
            ))}
          </li>
        ))}
      </ol>
      {canDecide && (ro.state === 'open' || ro.state === 'halted') ? (
        <div className="row-actions">
          {ro.state === 'halted' ? (
            <button className="btn-secondary" onClick={() => act('recheck')}>
              Recheck gates
            </button>
          ) : null}
          <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Reason to abort" aria-label="Reason to abort" maxLength={200} />
          <button className="btn-secondary" disabled={!reason.trim()} onClick={() => act('abort')}>
            Abort rollout
          </button>
        </div>
      ) : null}
    </Card>
  );
}

export default function Decision() {
  const [id, setId] = useState(decisionFromHash);
  useEffect(() => {
    const on = () => setId(decisionFromHash());
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  const { data, error } = useApi<DecisionResponse>(id ? `/api/v1/decisions/${encodeURIComponent(id)}` : null, 5000);
  const p = data?.decision;
  const decided = !!p?.outcome && p.outcome.state !== 'observing';
  const { data: why } = useApi<ExplanationResponse>(decided && id ? `/api/v1/proposals/${encodeURIComponent(id)}/explanation` : null);
  const { data: prec } = useApi<Precedents>(id ? `/api/v1/proposals/${encodeURIComponent(id)}/similar` : null);

  if (!id) return <Empty>No decision selected.</Empty>;
  return (
    <>
      <PageHero
        eyebrow="Decision record"
        title={p ? p.action_name : id}
        tint="purple"
        lede="What Zyntra saw, what it predicted, who approved, whether it still held when it ran, what ran, and what happened afterwards."
        actions={
          <>
            <a className="btn-secondary" href="#/approvals">
              <ArrowLeft size={14} aria-hidden /> Approvals
            </a>
            {p ? (
              <a className="btn-secondary" href={`/api/v1/decisions/${encodeURIComponent(p.id)}/export`} download>
                <Download size={14} aria-hidden /> Signed export
              </a>
            ) : null}
          </>
        }
      />
      <ErrorNote message={error} />
      {p ? (
        <>
          <div className="pills decision-pills">
            <Pill tone={phaseTone[p.phase] ?? 'neutral'}>{p.phase}</Pill>
            <Pill tone={riskTone(p.risk)}>{p.risk || 'low'} risk</Pill>
            {p.adapter ? <Pill tone="info">{p.adapter}</Pill> : null}
            <span className="mono small muted">{p.id}</span>
            {p.model_version ? <span className="mono small muted">model {p.model_version}</span> : null}
          </div>
          {data?.linked ? (
            <p className="info-note">
              {p.rollback_of ? 'Rolls back ' : 'Rollback proposal '}
              <button className="linklike" onClick={() => openDecision(data.linked!.id)}>
                {data.linked.action_name} ({data.linked.id})
              </button>{' '}
              — {data.linked.phase}
            </p>
          ) : null}

          <ol className="timeline">
            <Step title={`Proposed by ${p.created_by}`} at={p.created_at} tone="info">
              {p.inputs ? (
                <>
                  <p className="small">
                    Inputs captured {ago(p.inputs.at)}: {Object.keys(p.inputs.values).length} KPIs
                    {p.inputs.freshness?.some((f) => f.status === 'stale' || f.status === 'missing') ? (
                      <>
                        {' '}
                        · stale or missing:{' '}
                        {p.inputs.freshness
                          .filter((f) => f.status === 'stale' || f.status === 'missing')
                          .map((f) => f.kpi)
                          .join(', ')}
                      </>
                    ) : (
                      ' · all inputs fresh'
                    )}
                    {p.inputs.sources?.length ? ` · ${p.inputs.sources.filter((s) => s.ok).length}/${p.inputs.sources.length} sources healthy` : ''}
                  </p>
                </>
              ) : null}
              {p.simulation ? (
                <details className="trace">
                  <summary>
                    Prediction · weighted severity {sev(p.simulation.weighted_before ?? p.simulation.severity_before)} →{' '}
                    {sev(p.simulation.weighted_after ?? p.simulation.severity_after)}
                  </summary>
                  <SimResultView r={p.simulation} />
                </details>
              ) : (
                <p className="small">
                  Severity {sev(p.predicted.severity_before)} → {sev(p.predicted.severity_after)}
                </p>
              )}
              {p.alternatives?.length ? (
                <details className="trace">
                  <summary>Alternatives considered ({p.alternatives.length})</summary>
                  <ul className="small">
                    {p.alternatives.map((a) => (
                      <li key={a.action}>
                        {a.name} — score {a.score.toFixed(3)}
                        {a.confidence ? `, ${a.confidence} confidence` : ''}
                        {a.blocked_reasons?.length ? <span className="down"> · blocked: {a.blocked_reasons.join('; ')}</span> : null}
                      </li>
                    ))}
                  </ul>
                </details>
              ) : null}
              {p.evidence && Object.keys(p.evidence).length ? (
                <details className="trace">
                  <summary>Source rows behind the payload</summary>
                  {Object.entries(p.evidence).map(([act, ev]) => (
                    <div key={act} className="small">
                      {Object.entries(ev.rows ?? {}).map(([kpi, rows]) => (
                        <div key={kpi}>
                          <strong className="mono">{kpi}</strong>{' '}
                          <span className="muted">
                            {rows.length ? `${rows[0].source} rows ${rows.map((r) => r.index).join(', ')}` : 'no rows matched'}
                          </span>
                          {rows.length ? (
                            <table className="table compact">
                              <thead>
                                <tr>
                                  <th>#</th>
                                  {Object.keys(rows[0].values).sort().map((c) => (
                                    <th key={c}>{c}</th>
                                  ))}
                                </tr>
                              </thead>
                              <tbody>
                                {rows.slice(0, 20).map((r) => (
                                  <tr key={r.index}>
                                    <td className="mono">{r.index}</td>
                                    {Object.keys(rows[0].values).sort().map((c) => (
                                      <td key={c} className="mono">
                                        {String(r.values[c] ?? '')}
                                      </td>
                                    ))}
                                  </tr>
                                ))}
                              </tbody>
                            </table>
                          ) : null}
                        </div>
                      ))}
                      {Object.entries(ev.fills ?? {}).map(([n, f]) => (
                        <p key={n} className="small">
                          <span className="mono">fill:{n}</span> ← column <span className="mono">{f.column}</span>{' '}
                          {f.by === 'model' ? <Pill tone="purple">column chosen by model</Pill> : null} · {f.values.length} value(s) from {f.kpis.join(', ')}
                        </p>
                      ))}
                    </div>
                  ))}
                </details>
              ) : null}
              {prec?.items.length ? (
                <details className="trace" open>
                  <summary>Similar past decisions</summary>
                  <p className="small">{prec.text}</p>
                  <ul className="small">
                    {prec.items.map((x) => (
                      <li key={x.proposal}>
                        <button className="linklike" onClick={() => openDecision(x.proposal)}>
                          {x.text}
                        </button>{' '}
                        <span className="muted">overlap {x.overlap.join(', ') || 'action'}</span>
                      </li>
                    ))}
                  </ul>
                </details>
              ) : null}
              {p.policy ? (
                <p className="small muted">
                  Policy: {p.policy.approvals} approval{p.policy.approvals === 1 ? '' : 's'}
                  {p.policy.distinct_from_proposer ? ', not the proposer' : ''} · pending expires after {p.policy.pending_expiry} · approval
                  holds {p.policy.approved_expiry}
                  {p.policy.maintenance_windows?.length ? ` · windows ${p.policy.maintenance_windows.join(', ')}` : ''}
                  {p.policy.require_fresh ? ' · fresh inputs required' : ''} · Keep {p.policy.keep}
                  {p.policy.rules?.length ? ` · rules ${p.policy.rules.join(', ')}` : ''}
                </p>
              ) : null}
            </Step>

            {p.approvals.map((a, i) => (
              <Step key={`${a.by}-${i}`} title={`Approved by ${a.by}${a.role ? ` (${a.role})` : ''}`} at={a.at} tone="ok">
                <p className="small">
                  {p.required_approvals > 1 ? `Approval ${i + 1} of ${p.required_approvals}` : 'Approval'}
                  {a.method ? ` · via ${a.method}` : ''}
                  {a.reason ? ` — “${a.reason}”` : ''}
                </p>
              </Step>
            ))}
            {p.status === 'rejected' ? (
              <Step title={`Rejected by ${p.decided_by}`} at={p.decided_at} tone="neutral">
                {p.reason ? <p className="small">“{p.reason}”</p> : null}
              </Step>
            ) : null}
            {p.status === 'expired' ? <Step title="Expired" tone="neutral" /> : null}
            {p.waiting_for_window ? <Step title="Waiting for maintenance window" tone="warn" /> : null}

            {p.revalidation ? (
              <Step title={p.revalidation.ok ? 'Revalidated before execution' : 'Blocked at revalidation'} at={p.revalidation.at} tone={p.revalidation.ok ? 'ok' : 'bad'}>
                <p className="small">
                  Predicted weighted improvement now {p.revalidation.weighted_improvement.toFixed(3)} · drift {(p.revalidation.drift * 100).toFixed(0)}%
                  {p.revalidation.model_version ? ` · model ${p.revalidation.model_version}` : ''}
                </p>
                {p.revalidation.reasons?.length ? (
                  <ul className="small down">
                    {p.revalidation.reasons.map((r) => (
                      <li key={r}>{r}</li>
                    ))}
                  </ul>
                ) : null}
              </Step>
            ) : null}

            {p.execution ? (
              <Step title={`Executed (${p.execution.mode}) — ${p.execution.ok ? 'ok' : 'failed'}`} at={p.executed_at} tone={p.execution.ok ? 'ok' : 'bad'}>
                <details className="trace" open={!p.execution.ok}>
                  <summary>Command and output</summary>
                  <pre className="code">{[`$ kubectl ${p.execution.args.join(' ')}`, p.execution.output, p.execution.error].filter(Boolean).join('\n')}</pre>
                </details>
              </Step>
            ) : null}

            {p.outcome ? (
              <Step title={`Outcome: ${p.outcome.state}`} at={p.outcome.decided_at ?? p.outcome.started_at} tone={phaseTone[p.outcome.state as Phase] ?? 'neutral'}>
                <p className="small">
                  Window {p.outcome.window} · needs {p.outcome.required_samples} consecutive fresh samples meeting{' '}
                  {p.outcome.success_criteria.map((c) => `${c.kpi} ${c.op}${c.value !== undefined ? ` ${fmt(c.value)}` : ''}`).join(', ') || 'no criteria'}
                  {p.outcome.guardrails?.length ? ` · guardrails ${p.outcome.guardrails.join(', ')} (±${(p.outcome.tolerance * 100).toFixed(0)}%)` : ''}
                </p>
                {p.outcome.reasons?.length ? <p className="small">{p.outcome.reasons.join('; ')}</p> : null}
                {p.outcome.accuracy?.length ? (
                  <table className="table compact">
                    <thead>
                      <tr>
                        <th>KPI</th>
                        <th className="num">Before</th>
                        <th className="num">Predicted</th>
                        <th className="num">Actual</th>
                        <th>Prediction</th>
                      </tr>
                    </thead>
                    <tbody>
                      {p.outcome.accuracy.map((a) => (
                        <tr key={a.kpi}>
                          <td className="mono">{a.kpi}</td>
                          <td className="num">{fmt(a.baseline)}</td>
                          <td className="num">{fmt(a.predicted)}</td>
                          <td className="num">{fmt(a.actual)}</td>
                          <td>{a.hit ? <Pill tone="ok">hit</Pill> : <Pill tone="warn">off by {fmt(a.abs_error)}</Pill>}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : null}
                {why ? (
                  <div className="info-note small">
                    <strong>Why it {p.outcome.state === 'verified' ? 'landed' : 'missed'}</strong>{' '}
                    <Pill tone={why.verified ? 'ok' : 'bad'}>{why.stored ? (why.verified ? 'in audit chain' : 'hash mismatch') : 'not stored yet'}</Pill>
                    {why.mode === 'llm' ? <Pill tone="purple">worded by {why.model}</Pill> : null}
                    <p>{why.narrative}</p>
                    <ul>
                      {why.explanation.findings
                        .filter((f) => f.kind !== 'hit')
                        .map((f, i) => (
                          <li key={i}>
                            <Pill tone={f.kind.startsWith('stale') || f.kind === 'fallback-source' ? 'warn' : 'bad'}>{f.kind}</Pill> {f.text}
                          </li>
                        ))}
                    </ul>
                    <span className="mono muted">sha256 {why.explanation.hash.slice(0, 16)}</span>
                  </div>
                ) : null}
                {p.outcome.samples.length ? (
                  <table className="table compact">
                    <thead>
                      <tr>
                        <th>Sample</th>
                        <th>Values</th>
                        <th>Met</th>
                      </tr>
                    </thead>
                    <tbody>
                      {p.outcome.samples.slice(-10).map((s) => (
                        <tr key={s.at}>
                          <td title={s.at}>{ago(s.at)}</td>
                          <td className="mono small">
                            {Object.entries(s.values)
                              .map(([k, v]) => `${k}=${fmt(v)}`)
                              .join('  ')}
                            {s.stale?.length ? <span className="down"> stale: {s.stale.join(', ')}</span> : null}
                          </td>
                          <td>{s.breaches?.length ? <Pill tone="bad">breach</Pill> : s.met ? <Pill tone="ok">yes</Pill> : <Pill>no</Pill>}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : null}
              </Step>
            ) : null}
          </ol>

          {data?.decision.rollout ? <RolloutPanel id={data.decision.id} /> : null}
          <Card title="Audit trail" aside={<Pill>{data?.audit.length ?? 0} events</Pill>}>
            <table className="table compact">
              <thead>
                <tr>
                  <th>#</th>
                  <th>When</th>
                  <th>Transition</th>
                  <th>By</th>
                  <th>Note</th>
                  <th>Hash</th>
                </tr>
              </thead>
              <tbody>
                {data?.audit.map((e) => (
                  <tr key={e.seq ?? e.at}>
                    <td className="mono">{e.seq}</td>
                    <td title={e.at}>{ago(e.at)}</td>
                    <td>
                      {e.from && e.from !== e.to ? `${e.from} → ` : ''}
                      <strong>{e.to}</strong>
                      {e.phase ? <span className="muted"> · {e.phase}</span> : null}
                    </td>
                    <td>{e.by}</td>
                    <td className="muted">
                      {e.note}
                      {e.explanation_sha256 ? (
                        <div className="mono small" title={e.explanation_sha256}>
                          explanation {e.explanation_sha256.slice(0, 12)}
                        </div>
                      ) : null}
                      {e.payload_sha256 ? (
                        <div className="mono small" title={e.payload_sha256}>
                          payload {e.payload_sha256.slice(0, 12)}
                          {e.response_sha256 ? <span title={e.response_sha256}> · response {e.response_sha256.slice(0, 12)}</span> : null}
                        </div>
                      ) : null}
                    </td>
                    <td className="mono small muted" title={e.hash}>
                      {e.hash?.slice(0, 12)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        </>
      ) : null}
    </>
  );
}

import { fmt, type Contradiction, type EdgeProposal, type Model } from '../api';
import PackDraftCard from '../components/PackDraft';
import { useApi } from '../hooks';
import { Card, ErrorNote, KpiValue, PageHero, Pill, kpiMet, riskTone } from '../components/ui';

export default function ModelPage() {
  const { data, error } = useApi<{ model: Model; refreshed_at: string }>('/api/v1/graph', 30000);
  const m = data?.model;
  const name = (id: string) => m?.kpis.find((k) => k.id === id)?.name ?? id;
  const edges = useApi<{ edges: EdgeProposal[]; note: string }>('/api/v1/ai/edges', 60000);
  const rules = useApi<{ rules: number; mode: string; contradictions: Contradiction[]; llm_error?: string }>('/api/v1/ai/contradictions', 60000);

  return (
    <>
      <PageHero
        eyebrow="Model"
        title={m?.name ?? 'Model'}
        lede={
          m?.pack
            ? `Pack ${m.pack.id}${m.pack.version ? ` v${m.pack.version}` : ''}${m.pack.industry ? ` (${m.pack.industry})` : ''}: KPIs with targets, weighted cause-and-effect edges between them, and the actions Zyntra may propose.`
            : 'KPIs with targets, weighted cause-and-effect edges between them, and the actions Zyntra may propose.'
        }
      />
      <ErrorNote message={error} />
      {m ? (
        <>
          <Card title={`KPIs (${m.kpis.length})`}>
            <div className="kpi-grid">
              {m.kpis.map((k) => {
                const met = kpiMet(k);
                return (
                  <div key={k.id} className={`kpi-tile ${met === null ? '' : met ? 'ok' : 'warn'}`}>
                    <span className="muted small">{k.owner || 'kpi'}</span>
                    <strong>{k.name}</strong>
                    <span className="kpi-value">
                      <KpiValue k={k} />
                    </span>
                    <span className="muted small">
                      {k.target !== undefined ? `target ${k.direction === 'lower' ? '≤' : '≥'} ${fmt(k.target)}` : 'no target'}
                      {k.source ? ` · ${k.source.kind}` : ''}
                    </span>
                  </div>
                );
              })}
            </div>
          </Card>
          <div className="grid-2">
            <Card title={`Edges (${m.edges.length})`}>
              <table className="table compact">
                <thead>
                  <tr>
                    <th>From</th>
                    <th>To</th>
                    <th className="num">Weight</th>
                  </tr>
                </thead>
                <tbody>
                  {m.edges.map((e) => (
                    <tr key={`${e.from}-${e.to}`} title={e.why}>
                      <td>{name(e.from)}</td>
                      <td>{name(e.to)}</td>
                      <td className={`num ${e.weight >= 0 ? 'up' : 'down'}`}>{e.weight}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Card>
            <Card title={`Actions (${m.actions.length})`}>
              <ul className="list">
                {m.actions.map((a) => (
                  <li key={a.id}>
                    <div className="list-main">
                      <strong>{a.name}</strong>
                      <span className="muted">
                        {a.effects.map((e) => `${name(e.kpi)} ${e.change > 0 ? '+' : ''}${(e.change * 100).toFixed(0)}%`).join(' · ')}
                      </span>
                    </div>
                    <div className="pills">
                      {a.execute ? (
                        <Pill tone="info">{a.execute.template}</Pill>
                      ) : a.webhook ? (
                        <Pill tone="info">webhook</Pill>
                      ) : a.file ? (
                        <Pill tone="info">file</Pill>
                      ) : a.adapter === 'noop' ? (
                        <Pill>people</Pill>
                      ) : (
                        <Pill>advisory</Pill>
                      )}
                      {a.window ? <Pill tone="purple">{a.window}</Pill> : null}
                      {a.invariants?.length ? <Pill tone="warn">invariant</Pill> : null}
                      <Pill tone={riskTone(a.risk)}>{a.risk || 'low'}</Pill>
                    </div>
                  </li>
                ))}
              </ul>
            </Card>
          </div>
          <div className="grid-2">
            <Card title="Proposed edges" aside={<Pill tone="warn">not in the model</Pill>}>
              {edges.data?.edges?.length ? (
                <ul className="list">
                  {edges.data.edges.map((e) => (
                    <li key={`${e.from}-${e.to}`}>
                      <div className="list-main">
                        <strong>
                          {name(e.from)} → {name(e.to)}
                        </strong>
                        <span className="muted small">{e.why}</span>
                        <pre className="code small">{e.yaml}</pre>
                      </div>
                      <div className="pills">
                        <Pill tone="info">r {e.r.toFixed(2)}</Pill>
                        <Pill>{e.direction === 'leads' ? 'leads by one sample' : 'direction unknown'}</Pill>
                        <Pill tone="warn">{e.status}</Pill>
                      </div>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="small muted">
                  No KPI pair moves together without an edge yet. Needs at least 8 paired changes in the history (correlation 0.7 or more).
                </p>
              )}
            </Card>
            <Card title="Pack rules check" aside={rules.data ? <Pill tone={rules.data.mode === 'llm' ? 'purple' : 'neutral'}>{rules.data.rules} rule lines</Pill> : null}>
              {rules.data?.contradictions?.length ? (
                <ul className="list">
                  {rules.data.contradictions.map((c, i) => (
                    <li key={i}>
                      <div className="list-main">
                        <strong>{c.action}</strong>
                        <span className="small">{c.why}</span>
                        <span className="muted small">
                          README line {c.rule.line}: {c.rule.text}
                        </span>
                      </div>
                      <div className="pills">
                        <Pill tone={c.by === 'model' ? 'purple' : 'bad'}>{c.by === 'model' ? 'read by model' : 'rule check'}</Pill>
                      </div>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="small muted">No scored action breaks a rule stated in the pack README.</p>
              )}
              {rules.data?.llm_error ? <p className="small down">model: {rules.data.llm_error}</p> : null}
            </Card>
          </div>
          <PackDraftCard />
        </>
      ) : null}
    </>
  );
}

import { fmt, type Model } from '../api';
import { useApi } from '../hooks';
import { Card, ErrorNote, KpiValue, PageHero, Pill, kpiMet, riskTone } from '../components/ui';

export default function ModelPage() {
  const { data, error } = useApi<{ model: Model; refreshed_at: string }>('/api/v1/graph', 30000);
  const m = data?.model;
  const name = (id: string) => m?.kpis.find((k) => k.id === id)?.name ?? id;

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
        </>
      ) : null}
    </>
  );
}

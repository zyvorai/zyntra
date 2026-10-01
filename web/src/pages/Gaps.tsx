import { pct, sev, type Gap } from '../api';
import { useApi } from '../hooks';
import { Card, Empty, ErrorNote, Meter, PageHero, Stat } from '../components/ui';

export default function Gaps() {
  const { data, error } = useApi<{ gaps: Gap[]; severity_total: number }>('/api/v1/gaps', 15000);
  const gaps = data?.gaps ?? [];
  return (
    <>
      <PageHero
        eyebrow="Decide"
        title="Gaps"
        tint="amber"
        lede="KPIs that miss their target, ranked by relative shortfall. Severity 25% means the value is a quarter of the way off target."
      />
      <ErrorNote message={error} />
      <div className="stats">
        <Stat label="Gaps" value={data ? gaps.length : '—'} tone={gaps.length ? 'warn' : 'ok'} />
        <Stat label="Total severity" value={data ? sev(data.severity_total) : '—'} />
        <Stat label="Worst" value={gaps[0] ? gaps[0].name : '—'} sub={gaps[0] ? pct(gaps[0].severity) : undefined} />
      </div>
      <Card>
        {data && gaps.length === 0 ? (
          <Empty>No gaps. Every KPI with a target meets it.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>KPI</th>
                <th>Owner</th>
                <th className="num">Value</th>
                <th className="num">Target</th>
                <th>Severity</th>
              </tr>
            </thead>
            <tbody>
              {gaps.map((g) => (
                <tr key={g.kpi}>
                  <td>
                    <strong>{g.name}</strong>
                    <div className="muted mono">{g.kpi}</div>
                  </td>
                  <td>{g.owner || '—'}</td>
                  <td className="num">
                    {g.value.toFixed(2)} {g.unit}
                  </td>
                  <td className="num">
                    {g.direction === 'lower' ? '≤' : '≥'} {g.target} {g.unit}
                  </td>
                  <td>
                    <div className="sev">
                      <Meter value={g.severity} />
                      <span className="num">{pct(g.severity)}</span>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </>
  );
}

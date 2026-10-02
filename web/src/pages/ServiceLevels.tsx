import { fmt, type TenantKPI } from '../api';
import { useApi } from '../hooks';
import { Card, Empty, ErrorNote, PageHero, Pill, Stat } from '../components/ui';

/** A tenant's own service levels against their targets. */
export default function ServiceLevels() {
  const { data, error } = useApi<{ tenant: string; kpis: TenantKPI[] }>('/api/v1/tenant/kpis', 15000);
  const kpis = data?.kpis ?? [];
  const missing = kpis.filter((k) => !k.met).length;
  return (
    <>
      <PageHero
        eyebrow="Business"
        title="Service levels"
        tint="green"
        lede="Your service levels against their targets. Provider infrastructure is not shown; a degraded provider component appears only as a generic flag on the objects it affects."
      />
      <ErrorNote message={error} />
      <div className="stats">
        <Stat label="Service levels" value={data ? kpis.length : '—'} />
        <Stat label="Off target" value={data ? missing : '—'} tone={missing ? 'warn' : 'ok'} />
      </div>
      <Card>
        {data && kpis.length === 0 ? (
          <Empty>No service levels are defined for your workspace yet.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Service level</th>
                <th className="num">Now</th>
                <th className="num">Target</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {kpis.map((k) => (
                <tr key={k.id}>
                  <td>
                    <strong>{k.name}</strong>
                  </td>
                  <td className="num">
                    {fmt(k.value)} {k.unit}
                  </td>
                  <td className="num">
                    {k.target === undefined ? '—' : `${k.direction === 'lower' ? '≤' : '≥'} ${fmt(k.target)} ${k.unit ?? ''}`}
                  </td>
                  <td>
                    {k.stale ? <Pill tone="warn">stale</Pill> : k.met ? <Pill tone="ok">on target</Pill> : <Pill tone="bad">off target</Pill>}
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

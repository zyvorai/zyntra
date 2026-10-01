import { useMemo, useState } from 'react';
import { ago, fmt, type Model, type SourceStatus } from '../api';
import { useApi } from '../hooks';
import { Card, Empty, ErrorNote, KpiValue, PageHero, Pill, Sparkline, kpiMet } from '../components/ui';

type Hist = { kpi: string; points: { t: string; v: number }[] };

function KpiRow({ id }: { id: string }) {
  const h = useApi<Hist>(`/api/v1/kpis/${encodeURIComponent(id)}/history`, 30000);
  return <Sparkline points={(h.data?.points ?? []).slice(-60).map((p) => p.v)} />;
}

const vendor = (name: string) => {
  const n = name.toLowerCase();
  if (n.includes('netra')) return 'sky';
  if (n.includes('gravia')) return 'green';
  if (n.includes('fabric') || n.includes('keep')) return 'orange';
  return 'neutral';
};

export default function Signals() {
  const src = useApi<{ sources: SourceStatus[]; refreshed_at: string }>('/api/v1/sources', 15000);
  const graph = useApi<{ model: Model }>('/api/v1/graph', 15000);
  const [filter, setFilter] = useState('all');
  const sources = src.data?.sources ?? [];
  const kpis = graph.data?.model.kpis ?? [];

  const bySource = useMemo(() => {
    const m: Record<string, string> = {};
    for (const s of sources) for (const k of s.kpis ?? []) m[k] = s.name;
    return m;
  }, [sources]);

  const shown = filter === 'all' ? kpis : kpis.filter((k) => (bySource[k.id] ?? 'static') === filter);

  return (
    <>
      <PageHero
        eyebrow="Monitor"
        title="Signals"
        tint="green"
        lede="Netra eBPF datapath counters, Gravia GPU scheduling state and Fabric host KPIs — scraped live, converted to rates, and fed into the KPI graph."
      />
      <ErrorNote message={src.error || graph.error} />
      <div className="source-grid">
        {sources.length === 0 && src.data ? <Empty>No live sources configured.</Empty> : null}
        {sources.map((s) => (
          <button
            key={s.name}
            className={`source-card ${s.ok ? 'ok' : 'bad'}${filter === s.name ? ' selected' : ''}`}
            onClick={() => setFilter(filter === s.name ? 'all' : s.name)}
          >
            <div className="source-card-head">
              <span className={`vendor vendor-${vendor(s.name)}`} />
              <strong>{s.name}</strong>
              <Pill tone={s.ok ? 'ok' : 'bad'}>{s.ok ? 'healthy' : 'down'}</Pill>
            </div>
            <span className="muted small">
              {s.kind} · {s.latency_ms} ms · {s.kpis?.length ?? 0} KPIs · {ago(s.checked_at)}
            </span>
            {s.error ? <span className="down small truncate">{s.error}</span> : null}
          </button>
        ))}
      </div>

      <Card title={filter === 'all' ? 'All KPIs' : `KPIs from ${filter}`} aside={filter !== 'all' ? <button className="linklike" onClick={() => setFilter('all')}>Show all</button> : null}>
        {shown.length === 0 ? (
          <Empty>No KPIs.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>KPI</th>
                <th>Source</th>
                <th className="num">Value</th>
                <th className="num">Target</th>
                <th>Trend</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((k) => {
                const met = kpiMet(k);
                return (
                  <tr key={k.id}>
                    <td>
                      <strong>{k.name}</strong>
                      <div className="muted mono small">{k.source?.metric || k.source?.field || k.source?.query || k.id}</div>
                    </td>
                    <td>
                      {bySource[k.id] ? <Pill tone="info">{bySource[k.id]}</Pill> : <span className="muted">static</span>}
                      {k.source?.rate ? <span className="muted small"> /s</span> : null}
                    </td>
                    <td className="num">
                      <KpiValue k={k} />
                    </td>
                    <td className="num">{k.target !== undefined ? `${k.direction === 'lower' ? '≤' : '≥'} ${fmt(k.target)}` : '—'}</td>
                    <td>
                      <KpiRow id={k.id} />
                    </td>
                    <td>{met === null ? <span className="muted">—</span> : <Pill tone={met ? 'ok' : 'warn'}>{met ? 'met' : 'gap'}</Pill>}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </Card>
    </>
  );
}

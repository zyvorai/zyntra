import { duration, fmt, type AIStatus, type Anomaly, type Forecast } from '../api';
import { useApi } from '../hooks';
import type { Page } from '../nav';
import { Card, Empty, ErrorNote, PageHero, Pill } from '../components/ui';

const fcTone = { breach: 'bad', 'at-risk': 'warn', improving: 'ok', stable: 'neutral' } as const;

export default function Insights({ setPage }: { setPage: (p: Page) => void }) {
  const ins = useApi<{ anomalies: Anomaly[]; forecasts: Forecast[] }>('/api/v1/ai/insights', 15000);
  const st = useApi<AIStatus>('/api/v1/ai/status');
  const an = ins.data?.anomalies ?? [];
  const fc = (ins.data?.forecasts ?? []).filter((f) => f.status !== 'stable');

  return (
    <>
      <PageHero
        eyebrow="Intelligence"
        title="Insights"
        tint="purple"
        lede="Statistical detection over KPI history: z-score anomalies against a rolling baseline, and least-squares forecasts of when a KPI will cross its target."
        actions={
          <button className="btn-diag" onClick={() => setPage('ask')}>
            Ask Zyntra
          </button>
        }
      />
      <ErrorNote message={ins.error} />
      {st.data ? (
        <p className="muted small">
          AI mode <strong>{st.data.mode}</strong>
          {st.data.provider ? ` · ${st.data.provider}` : ''}
          {st.data.model ? ` · ${st.data.model}` : ''} · {st.data.mutations}
        </p>
      ) : null}
      <div className="grid-2">
        <Card title="Anomalies" aside={<Pill tone={an.length ? 'warn' : 'ok'}>{an.length}</Pill>}>
          {an.length === 0 ? (
            <Empty>No KPI is more than 3σ from its baseline.</Empty>
          ) : (
            <ul className="list">
              {an.map((a) => (
                <li key={a.kpi}>
                  <div className="list-main">
                    <strong>{a.name}</strong>
                    <span className="muted">{a.text}</span>
                  </div>
                  <Pill tone={a.severity === 'critical' ? 'bad' : 'warn'}>z {a.z.toFixed(1)}</Pill>
                </li>
              ))}
            </ul>
          )}
        </Card>
        <Card title="Forecasts" aside={<Pill tone={fc.some((f) => f.status === 'at-risk') ? 'warn' : 'neutral'}>{fc.length}</Pill>}>
          {fc.length === 0 ? (
            <Empty>No KPI is trending toward or away from its target yet. Forecasts need a few minutes of history.</Empty>
          ) : (
            <ul className="list">
              {fc.map((f) => (
                <li key={f.kpi}>
                  <div className="list-main">
                    <strong>{f.name}</strong>
                    <span className="muted">
                      {fmt(f.value)} → target {fmt(f.target)} · {f.slope_per_hour >= 0 ? '+' : ''}
                      {fmt(f.slope_per_hour)}/h
                      {f.eta_seconds !== undefined ? ` · ${f.status === 'at-risk' ? 'breach' : 'recovery'} in ${duration(f.eta_seconds)}` : ''}
                    </span>
                  </div>
                  <Pill tone={fcTone[f.status]}>{f.status}</Pill>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </>
  );
}

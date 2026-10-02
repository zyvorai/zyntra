import { useMemo, useState } from 'react';
import { ago, can, fmt, setManual, type InputsResponse, type ManualInput, type Model, type SourceStatus } from '../api';
import { useApi } from '../hooks';
import { useWho } from '../session';
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

const stateTone = { ok: 'ok', stale: 'warn', fallback: 'warn', error: 'bad' } as const;
const stateLabel = { ok: 'healthy', stale: 'stale', fallback: 'fallback', error: 'down' } as const;

function sourceState(s: SourceStatus) {
  const st = s.state ?? (s.ok ? 'ok' : 'error');
  return { tone: stateTone[st], label: stateLabel[st], cls: st === 'ok' ? 'ok' : st === 'error' ? 'bad' : 'warn' };
}

function ManualRow({ m, editable, onSaved }: { m: ManualInput; editable: boolean; onSaved: () => void }) {
  const [value, setValue] = useState('');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const save = async () => {
    const v = Number(value);
    if (value.trim() === '' || !Number.isFinite(v)) {
      setErr('Enter a number');
      return;
    }
    setBusy(true);
    setErr('');
    try {
      await setManual(m.kpi, v, reason);
      setValue('');
      setReason('');
      onSaved();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <tr>
      <td>
        <strong>{m.name}</strong>
        <div className="muted mono small">{m.kpi}</div>
      </td>
      <td>{m.owner || '—'}</td>
      <td className="num">{fmt(m.value, m.unit)}</td>
      <td className="small">{m.entry ? `${m.entry.by}, ${ago(m.entry.at)}${m.entry.reason ? `: ${m.entry.reason}` : ''}` : <span className="muted">never entered</span>}</td>
      <td>
        {editable ? (
          <div className="manual-form">
            <input type="number" step="any" value={value} placeholder="value" onChange={(e) => setValue(e.target.value)} aria-label={`New value for ${m.name}`} />
            <input type="text" value={reason} placeholder="reason (audited)" maxLength={500} onChange={(e) => setReason(e.target.value)} />
            <button className="primary" disabled={busy} onClick={save}>
              {busy ? 'Saving…' : 'Record'}
            </button>
            {err ? <span className="down small">{err}</span> : null}
          </div>
        ) : (
          <span className="muted small">needs proposer role</span>
        )}
      </td>
    </tr>
  );
}

export default function Signals() {
  const src = useApi<{ sources: SourceStatus[]; refreshed_at: string }>('/api/v1/sources', 15000);
  const graph = useApi<{ model: Model }>('/api/v1/graph', 15000);
  const inputs = useApi<InputsResponse>('/api/v1/inputs', 30000);
  const who = useWho();
  const manual = inputs.data?.manual ?? [];
  const channels = inputs.data?.webhooks ?? [];
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
        lede="Live sources feed the KPI graph: Prometheus and product APIs, file exports, sheets, webhooks and values people enter. A source that fails keeps its last value and is marked fallback; old data is marked stale."
      />
      <ErrorNote message={src.error || graph.error} />
      <div className="source-grid">
        {sources.length === 0 && src.data ? <Empty>No live sources configured.</Empty> : null}
        {sources.map((s) => {
          const st = sourceState(s);
          return (
          <button
            key={s.name}
            className={`source-card ${st.cls}${filter === s.name ? ' selected' : ''}`}
            onClick={() => setFilter(filter === s.name ? 'all' : s.name)}
          >
            <div className="source-card-head">
              <span className={`vendor vendor-${vendor(s.name)}`} />
              <strong>{s.name}</strong>
              <Pill tone={st.tone}>{st.label}</Pill>
            </div>
            <span className="muted small">
              {s.kind} · {s.latency_ms} ms · {s.kpis?.length ?? 0} KPIs · {ago(s.checked_at)}
            </span>
            {s.error ? <span className="down small truncate">{s.error}</span> : null}
          </button>
          );
        })}
      </div>

      {manual.length ? (
        <Card title="Manual values" aside={<span className="muted small">Every entry is written to the audit chain</span>}>
          <table className="table compact">
            <thead>
              <tr>
                <th>KPI</th>
                <th>Owner</th>
                <th className="num">Current</th>
                <th>Last entry</th>
                <th>New value</th>
              </tr>
            </thead>
            <tbody>
              {manual.map((m) => (
                <ManualRow key={m.kpi} m={m} editable={can(who, 'propose')} onSaved={() => { inputs.reload(); graph.reload(); }} />
              ))}
            </tbody>
          </table>
        </Card>
      ) : null}

      {channels.length ? (
        <Card title="Webhook inputs">
          <p className="muted small">
            Gateways POST JSON to <code>/api/v1/ingest/&lt;channel&gt;</code> with the ingest token. The last document wins.
          </p>
          <table className="table compact">
            <thead>
              <tr>
                <th>Channel</th>
                <th>KPIs</th>
                <th>Last received</th>
              </tr>
            </thead>
            <tbody>
              {channels.map((c) => (
                <tr key={c.name}>
                  <td className="mono">{c.name}</td>
                  <td className="small">{c.kpis.join(', ')}</td>
                  <td className="small">{c.received_at ? `${ago(c.received_at)}${c.from ? ` from ${c.from}` : ''}` : <span className="muted">nothing yet</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      ) : null}

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
                      <div className="muted mono small">{k.source?.metric || k.source?.file || k.source?.field || k.source?.query || k.source?.name || k.id}</div>
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

import { useState } from 'react';
import { Sparkles } from 'lucide-react';
import { ago, pct, type Answer, type Model, type Pulse } from '../api';
import { useApi } from '../hooks';
import type { Page } from '../nav';
import { Card, Empty, Meter, PageHero, Pill, Stat, riskTone } from '../components/ui';

export default function Overview({ pulse, setPage }: { pulse: Pulse | null; setPage: (p: Page) => void }) {
  const [owner, setOwner] = useState('');
  const [win, setWin] = useState('');
  const graph = useApi<{ model: Model }>('/api/v1/graph', 60000);
  const owners = graph.data?.model.pack?.owners ?? [];
  const windows = Object.keys(graph.data?.model.calendars ?? {});
  const q = new URLSearchParams();
  if (owner) q.set('owner', owner);
  if (win) q.set('window', win);
  const digest = useApi<Answer>(`/api/v1/ai/digest${q.size ? `?${q}` : ''}`, 30000);
  const sources = pulse?.sources ?? [];
  const healthy = sources.filter((s) => s.ok).length;
  const gaps = pulse?.gaps ?? [];
  const top = pulse?.top ?? [];

  return (
    <>
      <PageHero
        eyebrow="Decision intelligence"
        title={gaps.length ? `${gaps.length} KPI${gaps.length === 1 ? '' : 's'} off target` : 'Every KPI on target'}
        lede="Live signals from Netra eBPF, Gravia and Fabric, propagated through your KPI graph into ranked, human-approved actions."
      />

      <div className="stats">
        <Stat
          label="Total severity"
          value={pulse ? pulse.severity_total.toFixed(2) : '—'}
          tone={!pulse ? undefined : pulse.severity_total > 0.5 ? 'bad' : pulse.severity_total > 0 ? 'warn' : 'ok'}
          sub="sum of relative shortfalls"
        />
        <Stat label="Gaps" value={pulse ? gaps.length : '—'} tone={gaps.length ? 'warn' : 'ok'} sub="KPIs missing target" />
        <Stat
          label="Pending approvals"
          value={pulse ? pulse.pending_approvals : '—'}
          tone={pulse?.pending_approvals ? 'info' : undefined}
          sub={<button className="linklike" onClick={() => setPage('approvals')}>Open inbox</button>}
        />
        <Stat
          label="Sources"
          value={pulse ? `${healthy}/${sources.length}` : '—'}
          tone={sources.length && healthy < sources.length ? 'bad' : 'ok'}
          sub={pulse ? `updated ${ago(pulse.at)}` : 'connecting…'}
        />
        <Stat label="Anomalies" value={pulse ? pulse.anomalies : '—'} tone={pulse?.anomalies ? 'warn' : undefined} sub="|z| > 3 vs baseline" />
      </div>

      <Card
        className="digest-card"
        title={
          <>
            <Sparkles size={16} aria-hidden /> {win ? 'Close-of-window note' : 'Zyntra digest'}
          </>
        }
        aside={digest.data ? <Pill tone={digest.data.mode === 'llm' ? 'purple' : 'neutral'}>{digest.data.mode === 'llm' ? digest.data.model || 'LLM' : 'grounded'}</Pill> : null}
      >
        {owners.length || windows.length ? (
          <div className="row-actions">
            <select aria-label="Owner" value={owner} onChange={(e) => setOwner(e.target.value)}>
              <option value="">All owners</option>
              {owners.map((o) => (
                <option key={o}>{o}</option>
              ))}
            </select>
            {windows.length ? (
              <select aria-label="Window" value={win} onChange={(e) => setWin(e.target.value)}>
                <option value="">Now</option>
                {windows.map((w) => (
                  <option key={w} value={w}>
                    close of {w}
                  </option>
                ))}
              </select>
            ) : null}
          </div>
        ) : null}
        {digest.data ? <p className="prose">{digest.data.text}</p> : <Empty>{digest.error || 'Summarising…'}</Empty>}
        <div className="row-actions">
          <button className="btn-diag" onClick={() => setPage('ask')}>
            Ask a follow-up
          </button>
        </div>
      </Card>

      <div className="grid-2">
        <Card title="Gaps" aside={<button className="linklike" onClick={() => setPage('gaps')}>All gaps</button>}>
          {gaps.length === 0 ? (
            <Empty>No gaps — every KPI with a target meets it.</Empty>
          ) : (
            <ul className="list">
              {gaps.slice(0, 6).map((g) => (
                <li key={g.kpi}>
                  <div className="list-main">
                    <strong>{g.name}</strong>
                    <span className="muted">
                      {g.value.toFixed(2)} {g.unit} vs target {g.direction === 'lower' ? '≤' : '≥'} {g.target} {g.unit}
                    </span>
                  </div>
                  <Meter value={g.severity} />
                  <span className="num">{pct(g.severity)}</span>
                </li>
              ))}
            </ul>
          )}
        </Card>
        <Card title="Recommended actions" aside={<button className="linklike" onClick={() => setPage('plan')}>Full plan</button>}>
          {top.length === 0 ? (
            <Empty>Nothing to recommend right now.</Empty>
          ) : (
            <ul className="list">
              {top.map((r) => (
                <li key={r.action}>
                  <span className="rank">{r.rank}</span>
                  <div className="list-main">
                    <strong>{r.name}</strong>
                    <span className="muted">
                      closes {r.result.gaps_closed?.length ?? 0} · improves {pct(r.improvement)}
                    </span>
                  </div>
                  <Pill tone={riskTone(r.risk)}>{r.risk || 'low'}</Pill>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      <Card title="Signal sources" aside={<button className="linklike" onClick={() => setPage('signals')}>Details</button>}>
        {sources.length === 0 ? (
          <Empty>No live sources configured — the model runs on its static values.</Empty>
        ) : (
          <div className="source-strip">
            {sources.map((s) => (
              <div key={s.name} className={`source-tile ${s.ok ? 'ok' : 'bad'}`} title={s.error || `${s.latency_ms} ms`}>
                <span className={`dot ${s.ok ? 'ok' : 'critical'}`} aria-hidden />
                <strong>{s.name}</strong>
                <span className="muted">
                  {s.kind} · {s.ok ? `${s.latency_ms} ms` : 'down'}
                </span>
              </div>
            ))}
          </div>
        )}
      </Card>
    </>
  );
}

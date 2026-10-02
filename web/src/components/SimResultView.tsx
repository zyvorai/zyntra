import { fmt, sev, type SimResult } from '../api';
import { Pill } from './ui';

export default function SimResultView({ r }: { r: SimResult }) {
  const changed = r.kpis.filter((k) => k.change !== 0);
  const banded = changed.some((k) => k.low !== undefined && k.high !== undefined && k.low !== k.high);
  return (
    <div className="sim">
      <div className="sim-summary">
        <span>
          Severity <strong>{sev(r.severity_before)}</strong> → <strong>{sev(r.severity_after)}</strong>
        </span>
        {r.weighted_before !== undefined && r.weighted_before !== r.severity_before ? (
          <span>
            Weighted <strong>{sev(r.weighted_before)}</strong> → <strong>{sev(r.weighted_after ?? 0)}</strong>
          </span>
        ) : null}
        {r.settles_after ? <span>settles in {r.settles_after}</span> : null}
        {r.gaps_closed?.map((g) => (
          <Pill key={`c-${g}`} tone="ok">
            closes {g}
          </Pill>
        ))}
        {r.gaps_opened?.map((g) => (
          <Pill key={`o-${g}`} tone="bad">
            opens {g}
          </Pill>
        ))}
      </div>
      <table className="table compact">
        <thead>
          <tr>
            <th>KPI</th>
            <th className="num">Before</th>
            <th className="num">After</th>
            {banded ? <th className="num">Range</th> : null}
            <th className="num">Change</th>
            <th>Target</th>
          </tr>
        </thead>
        <tbody>
          {changed.map((k) => (
            <tr key={k.kpi}>
              <td>{k.name}</td>
              <td className="num">{fmt(k.before, k.unit)}</td>
              <td className="num">
                {fmt(k.after, k.unit)}
                {k.bounded ? <span className="band"> (bounded)</span> : null}
              </td>
              {banded ? (
                <td className="num band">{k.low !== undefined && k.high !== undefined && k.low !== k.high ? `${fmt(k.low)}–${fmt(k.high)}` : '—'}</td>
              ) : null}
              <td className={`num ${k.change > 0 ? 'up' : 'down'}`}>
                {k.change > 0 ? '+' : ''}
                {(k.change * 100).toFixed(1)}%
              </td>
              <td>
                {!k.has_target ? (
                  <span className="muted">—</span>
                ) : k.met_after ? (
                  <Pill tone="ok">{k.met_before ? 'met' : 'now met'}</Pill>
                ) : (
                  <Pill tone={k.met_before ? 'bad' : 'warn'}>{k.met_before ? 'now missed' : 'missed'}</Pill>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {r.violations?.length ? (
        <div className="error-note">
          <strong>Breaks hard constraints:</strong>
          <ul className="blocked-list">
            {r.violations.map((v) => (
              <li key={v.text}>{v.text}</li>
            ))}
          </ul>
        </div>
      ) : null}
      {r.stale_inputs?.length ? <p className="muted small">Depends on stale inputs: {r.stale_inputs.join(', ')}</p> : null}
      {r.trace.length ? (
        <details className="trace">
          <summary>Why — propagation trace ({r.trace.length} steps)</summary>
          <ol>
            {r.trace.map((s, i) => (
              <li key={i}>{s.text}</li>
            ))}
          </ol>
        </details>
      ) : null}
    </div>
  );
}

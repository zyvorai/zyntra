import { fmt, sev, type SimResult } from '../api';
import { Pill } from './ui';

export default function SimResultView({ r }: { r: SimResult }) {
  const changed = r.kpis.filter((k) => k.change !== 0);
  return (
    <div className="sim">
      <div className="sim-summary">
        <span>
          Severity <strong>{sev(r.severity_before)}</strong> → <strong>{sev(r.severity_after)}</strong>
        </span>
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
            <th className="num">Change</th>
            <th>Target</th>
          </tr>
        </thead>
        <tbody>
          {changed.map((k) => (
            <tr key={k.kpi}>
              <td>{k.name}</td>
              <td className="num">{fmt(k.before, k.unit)}</td>
              <td className="num">{fmt(k.after, k.unit)}</td>
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

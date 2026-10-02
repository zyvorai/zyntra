import { useState } from 'react';
import { api, fmt, type Comparison, type Scenario } from '../api';
import { useApi } from '../hooks';
import { Card, Empty, ErrorNote, PageHero, Pill } from '../components/ui';

export default function Scenarios() {
  const list = useApi<{ scenarios: Scenario[] }>('/api/v1/scenarios');
  const graph = useApi<{ model: { actions: { id: string; name: string }[] } }>('/api/v1/graph');
  const [name, setName] = useState('');
  const [picked, setPicked] = useState<string[]>([]);
  const [assume, setAssume] = useState('');
  const [chosen, setChosen] = useState<string[]>([]);
  const [cmp, setCmp] = useState<Comparison | null>(null);
  const [err, setErr] = useState('');

  const create = async () => {
    setErr('');
    const assumptions: Record<string, number> = {};
    for (const part of assume.split(',').map((s) => s.trim()).filter(Boolean)) {
      const [k, v] = part.split('=');
      if (!k || Number.isNaN(Number(v))) return setErr(`Assumption "${part}" must look like kpi=value`);
      assumptions[k.trim()] = Number(v);
    }
    try {
      await api('/api/v1/scenarios', { method: 'POST', json: { name, actions: picked, assumptions } });
      setName('');
      setPicked([]);
      setAssume('');
      list.reload();
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const compare = async (ids: string[]) => {
    setChosen(ids);
    if (ids.length < 2) return setCmp(null);
    try {
      setCmp(await api<Comparison>(`/api/v1/scenarios/compare?ids=${ids.join(',')}`));
    } catch (e) {
      setErr((e as Error).message);
    }
  };
  const toggle = (id: string) => compare(chosen.includes(id) ? chosen.filter((x) => x !== id) : [...chosen, id]);
  const rerun = async (id: string) => {
    try {
      await api(`/api/v1/scenarios/${id}/run`, { method: 'POST' });
      list.reload();
      if (chosen.length > 1) compare(chosen);
    } catch (e) {
      setErr((e as Error).message);
    }
  };
  const scs = list.data?.scenarios ?? [];
  const names = Object.fromEntries(scs.map((s) => [s.id, s.name]));

  return (
    <>
      <PageHero
        eyebrow="Business"
        title="Scenarios"
        tint="green"
        lede="Save a plan with its assumptions, run it against the current model and data, and compare plans side by side. Running a scenario never changes anything."
      />
      <ErrorNote message={list.error || err} />
      <Card title="New scenario">
        <div className="form-grid">
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Name, e.g. Add GPUs and reroute" maxLength={80} aria-label="Scenario name" />
          <input value={assume} onChange={(e) => setAssume(e.target.value)} placeholder="Assumptions: kpi=value, kpi=value" aria-label="Assumptions" />
        </div>
        <div className="chips">
          {graph.data?.model.actions.map((a) => (
            <label key={a.id} className="check">
              <input type="checkbox" checked={picked.includes(a.id)} onChange={() => setPicked((p) => (p.includes(a.id) ? p.filter((x) => x !== a.id) : [...p, a.id]))} />{' '}
              {a.name || a.id}
            </label>
          ))}
        </div>
        <button className="primary" disabled={!name.trim() || picked.length === 0} onClick={create}>
          Save and run
        </button>
      </Card>
      <Card title="Saved scenarios" aside={<span className="muted small">tick two or more to compare</span>}>
        {scs.length === 0 ? (
          <Empty>No scenarios yet.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th />
                <th>Scenario</th>
                <th>Closes</th>
                <th>Objects at risk</th>
                <th>Ran on</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {scs.map((s) => (
                <tr key={s.id}>
                  <td>
                    <input type="checkbox" aria-label={`Compare ${s.name}`} checked={chosen.includes(s.id)} onChange={() => toggle(s.id)} />
                  </td>
                  <td>
                    <strong>{s.name}</strong>
                    <div className="muted mono small">{s.actions.join(' + ')}</div>
                  </td>
                  <td>{s.result?.gaps_closed?.join(', ') || '—'}</td>
                  <td>
                    {s.result ? `${s.result.at_risk_before?.length ?? 0} → ${s.result.at_risk_after?.length ?? 0}` : '—'}
                    {s.result?.blocked?.length ? <Pill tone="bad">blocked</Pill> : null}
                  </td>
                  <td className="small" title={`model ${s.model_version} · data ${s.data_version?.slice(0, 12)}`}>
                    {s.ran_at ? new Date(s.ran_at).toLocaleString() : 'never'}
                  </td>
                  <td>
                    <button className="btn-secondary" onClick={() => rerun(s.id)}>
                      Re-run
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
      {cmp ? (
        <Card title="Comparison">
          <table className="table">
            <thead>
              <tr>
                <th>KPI</th>
                <th className="num">Before</th>
                {cmp.scenarios.map((id) => (
                  <th key={id} className="num">
                    {names[id] ?? id}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {cmp.rows
                .filter((r) => r.after.some((v) => v !== r.before))
                .map((r) => (
                  <tr key={r.kpi}>
                    <td>{r.name || r.kpi}</td>
                    <td className="num">{fmt(r.before)}</td>
                    {r.after.map((v, i) => (
                      <td key={i} className="num">
                        {fmt(v)} {r.met[i] ? '' : <span className="down">miss</span>}
                      </td>
                    ))}
                  </tr>
                ))}
              <tr>
                <td>Weighted severity after</td>
                <td />
                {cmp.weighted_after.map((v, i) => (
                  <td key={i} className="num">
                    {fmt(v)}
                  </td>
                ))}
              </tr>
              <tr>
                <td>Objects at risk after</td>
                <td />
                {cmp.objects_at_risk_after.map((v, i) => (
                  <td key={i} className="num">
                    {v}
                  </td>
                ))}
              </tr>
              <tr>
                <td>Objects exposed after</td>
                <td />
                {cmp.objects_exposed_after.map((v, i) => (
                  <td key={i} className="num">
                    {v}
                  </td>
                ))}
              </tr>
            </tbody>
          </table>
        </Card>
      ) : null}
    </>
  );
}

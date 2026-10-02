import { useState } from 'react';
import { api, type Action, type Model, type SimResult } from '../api';
import { useApi } from '../hooks';
import SimResultView from '../components/SimResultView';
import { Card, ErrorNote, PageHero } from '../components/ui';

type Mode = 'action' | 'custom';

export default function Simulate() {
  const graph = useApi<{ model: Model }>('/api/v1/graph');
  const model = graph.data?.model;
  const [mode, setMode] = useState<Mode>('action');
  const [action, setAction] = useState('');
  const [kpi, setKpi] = useState('');
  const [change, setChange] = useState('-10');
  const [result, setResult] = useState<SimResult | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const run = async () => {
    setBusy(true);
    setError('');
    try {
      let body: { action?: string; custom?: Action };
      if (mode === 'action') {
        body = { action: action || model?.actions[0]?.id };
      } else {
        const k = kpi || model?.kpis[0]?.id || '';
        const c = Number(change) / 100;
        if (!Number.isFinite(c)) throw new Error('change must be a number');
        body = { custom: { id: 'what-if', name: `What if ${k} changes ${change}%`, effects: [{ kpi: k, change: c }] } };
      }
      setResult(await api<SimResult>('/api/v1/simulate', { method: 'POST', json: body }));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <PageHero
        eyebrow="Decide"
        title="Simulate"
        tint="purple"
        lede="Propagate a change through the weighted KPI graph and see which targets it closes or opens — before anything touches the cluster."
      />
      <Card>
        <div className="segmented" role="tablist">
          <button role="tab" aria-selected={mode === 'action'} onClick={() => setMode('action')}>
            Model action
          </button>
          <button role="tab" aria-selected={mode === 'custom'} onClick={() => setMode('custom')}>
            Custom what-if
          </button>
        </div>
        <div className="toolbar">
          {mode === 'action' ? (
            <label>
              Action
              <select value={action} onChange={(e) => setAction(e.target.value)}>
                {model?.actions?.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </select>
            </label>
          ) : (
            <>
              <label>
                KPI
                <select value={kpi} onChange={(e) => setKpi(e.target.value)}>
                  {model?.kpis?.map((k) => (
                    <option key={k.id} value={k.id}>
                      {k.name}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                Relative change %
                <input type="number" value={change} onChange={(e) => setChange(e.target.value)} step="1" />
              </label>
            </>
          )}
          <button className="primary" onClick={run} disabled={busy || !model}>
            {busy ? 'Simulating…' : 'Simulate'}
          </button>
        </div>
        <ErrorNote message={error || graph.error} />
      </Card>
      {result ? (
        <Card title={result.action_name}>
          <SimResultView r={result} />
        </Card>
      ) : null}
    </>
  );
}

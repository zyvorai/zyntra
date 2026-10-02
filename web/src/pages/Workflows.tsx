import { useState } from 'react';
import { api, type OntSchema, type OntViewRow, type OntViewSpec, type Proposal } from '../api';
import { useApi } from '../hooks';
import { display } from '../format';
import { openObject } from '../nav';
import { Card, Empty, ErrorNote, PageHero, Pill, Stat } from '../components/ui';

type Page = import('../nav').Page;

export default function Workflows({ setPage }: { setPage?: (p: Page) => void }) {
  const schema = useApi<OntSchema>('/api/v1/ontology/schema');
  const views = schema.data?.views ?? [];
  const [sel, setSel] = useState('');
  const id = sel || views[0]?.id || '';
  const view = useApi<{ view: OntViewSpec; rows: OntViewRow[]; total?: number; truncated?: boolean }>(id ? `/api/v1/ontology/views/${id}` : null, 15000);
  const [note, setNote] = useState('');
  const [err, setErr] = useState('');
  const rows = view.data?.rows ?? [];
  const cols = view.data?.view.columns ?? [];
  const flagged = rows.filter((r) => r.failing_kpis?.length || r.exposed_by?.length).length;

  const propose = async (action: string, row: OntViewRow) => {
    const t = schema.data?.actions?.find((a) => a.id === action);
    const input = t?.inputs?.[0];
    if (!input) return;
    setErr('');
    setNote('');
    try {
      const p = await api<Proposal>('/api/v1/proposals', { method: 'POST', json: { action, inputs: { [input.name]: row.id } } });
      setNote(`Proposal ${p.id} created for ${row.id}. It now waits for approval.`);
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  return (
    <>
      <PageHero
        eyebrow="Business"
        title="Workflows"
        tint="amber"
        lede="Which business objects sit behind a failing KPI, and the typed actions you can propose for them. Every action still needs approval and runs dry by default."
      />
      <ErrorNote message={schema.error || view.error || err} />
      {note ? (
        <p className="info-note">
          {note}{' '}
          {setPage ? (
            <button className="linklike" onClick={() => setPage('approvals')}>
              Open approvals
            </button>
          ) : null}
        </p>
      ) : null}
      {views.length > 1 ? (
        <div className="segmented" role="tablist">
          {views.map((v) => (
            <button key={v.id} role="tab" aria-selected={v.id === id} onClick={() => setSel(v.id)}>
              {v.title}
            </button>
          ))}
        </div>
      ) : null}
      <div className="stats">
        <Stat label={view.data?.view.title ?? 'Objects'} value={view.data ? rows.length : '—'} />
        <Stat label="Exposed or failing" value={view.data ? flagged : '—'} tone={flagged ? 'warn' : 'ok'} />
      </div>
      {view.data?.truncated ? (
        <p className="info-note">
          Showing {rows.length.toLocaleString()} of {(view.data.total ?? rows.length).toLocaleString()} objects, those needing attention first. Narrow the view in ontology.yaml to see the rest.
        </p>
      ) : null}
      <Card>
        {view.data && rows.length === 0 ? (
          <Empty>No objects in this view.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Object</th>
                {cols.filter((c) => c !== 'name').map((c) => (
                  <th key={c}>{c}</th>
                ))}
                <th>Status</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id}>
                  <td>
                    <button className="linklike" onClick={() => openObject(r.id)}>
                      <strong>{String(r.cells.name ?? r.id)}</strong>
                    </button>
                    <div className="muted mono small">{r.id}</div>
                  </td>
                  {cols.filter((c) => c !== 'name').map((c) => (
                    <td key={c}>{r.cells[c] === undefined ? '—' : display(r.cells[c])}</td>
                  ))}
                  <td>
                    {r.failing_kpis?.length ? <Pill tone="bad">failing {r.failing_kpis.join(', ')}</Pill> : null}
                    {r.exposed_by?.length ? <Pill tone="warn">exposed via {r.exposed_by.join(', ')}</Pill> : null}
                    {!r.failing_kpis?.length && !r.exposed_by?.length ? <Pill tone="ok">clear</Pill> : null}
                  </td>
                  <td>
                    {view.data?.view.actions?.map((a) => (
                      <button key={a} className="btn-secondary" onClick={() => propose(a, r)}>
                        Propose {a}
                      </button>
                    ))}
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

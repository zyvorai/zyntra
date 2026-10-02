import { useEffect, useState } from 'react';
import { api, type Candidate, type OntChange, type OntObject, type OntObjectDetail, type OntSchema } from '../api';
import { useApi } from '../hooks';
import { objectFromHash, openObject } from '../nav';
import { Card, Empty, ErrorNote, PageHero, Pill } from '../components/ui';

const nameOf = (o: OntObject) => String(o.props.name?.v ?? o.id);
const when = (s: string) => new Date(s).toLocaleString();

function Provenance({ o }: { o: OntObject }) {
  return (
    <table className="table compact">
      <thead>
        <tr>
          <th>Property</th>
          <th>Value</th>
          <th>Source</th>
          <th>Observed</th>
        </tr>
      </thead>
      <tbody>
        {Object.entries(o.props).map(([k, v]) => (
          <tr key={k}>
            <td className="mono">{k}</td>
            <td>{String(v.v)}</td>
            <td className="mono small" title={v.prov.transform?.join(' → ')}>
              {v.prov.source}
              {v.prov.source_id ? <div className="muted">{v.prov.source_id}</div> : null}
            </td>
            <td className="small">{when(v.prov.observed_at)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function History({ id }: { id: string }) {
  const { data } = useApi<{ changes: OntChange[] }>(`/api/v1/ontology/objects/${encodeURIComponent(id)}/history`);
  const changes = [...(data?.changes ?? [])].reverse();
  if (!changes.length) return null;
  return (
    <details className="trace">
      <summary>Change history ({changes.length})</summary>
      <table className="table compact">
        <thead>
          <tr>
            <th>When</th>
            <th>Property</th>
            <th>Was</th>
            <th>Now</th>
            <th>Source</th>
          </tr>
        </thead>
        <tbody>
          {changes.map((c, i) => (
            <tr key={i}>
              <td className="small">{when(c.after.prov.ingested_at)}</td>
              <td className="mono">{c.property}</td>
              <td>{c.before ? String(c.before.v) : <span className="muted">new</span>}</td>
              <td>{String(c.after.v)}</td>
              <td className="mono small">{c.after.prov.source}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </details>
  );
}

function Detail({ id }: { id: string }) {
  const { data, error } = useApi<OntObjectDetail>(`/api/v1/ontology/objects/${encodeURIComponent(id)}`, 15000);
  if (error) return <ErrorNote message={error} />;
  if (!data) return <p className="muted">Loading…</p>;
  const { object: o } = data;
  return (
    <Card
      title={nameOf(o)}
      aside={
        <span className="pills">
          <Pill>{o.type}</Pill>
          {data.failing_kpis?.length ? <Pill tone="bad">failing {data.failing_kpis.join(', ')}</Pill> : null}
        </span>
      }
    >
      <p className="muted mono small">{o.id}</p>
      <Provenance o={o} />
      <History id={id} />
      {o.aliases?.length ? (
        <p className="small">
          Also known as:{' '}
          {o.aliases.map((a) => (
            <span key={a.system + a.external_id} className="mono">
              {a.system}:{a.external_id}{' '}
            </span>
          ))}
        </p>
      ) : null}
      <h3>Links</h3>
      {data.links.length === 0 ? (
        <Empty>No links you can see.</Empty>
      ) : (
        <div className="chips">
          {data.links.map((l) => (
            <button key={l.link.id} className="btn-secondary" onClick={() => openObject(l.other.id)}>
              {l.out ? `${l.link.type} →` : `← ${l.link.type}`} {nameOf(l.other)}
            </button>
          ))}
        </div>
      )}
      <h3>Depends on this</h3>
      <p className="muted small">Reachability through links, not a prediction of how much each would be affected.</p>
      {data.impact.length === 0 ? (
        <Empty>Nothing visible depends on this object.</Empty>
      ) : (
        <ul className="plain">
          {data.impact.map((i) => (
            <li key={i.object.id} style={{ marginLeft: (i.depth - 1) * 16 }}>
              <button className="linklike" onClick={() => openObject(i.object.id)}>
                {nameOf(i.object)}
              </button>{' '}
              <span className="muted small">
                {i.object.type} via {i.via}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function Resolution() {
  const { data, reload } = useApi<{ candidates: Candidate[] }>('/api/v1/ontology/resolution');
  const [err, setErr] = useState('');
  const pending = (data?.candidates ?? []).filter((c) => c.status === 'pending');
  if (!pending.length) return null;
  const decide = async (id: string, verb: 'accept' | 'reject') => {
    try {
      await api(`/api/v1/ontology/resolution/${id}/${verb}`, { method: 'POST' });
      setErr('');
      reload();
    } catch (e) {
      setErr((e as Error).message);
    }
  };
  return (
    <Card title="Same thing, two systems?" aside={<Pill tone="warn">{pending.length} to review</Pill>}>
      <p className="muted small">Uncertain matches are never merged automatically. Accepting merges the second object into the first and is audited.</p>
      <ErrorNote message={err} />
      {pending.map((c) => (
        <div key={c.id} className="row-actions">
          <span>
            <span className="mono">{c.a}</span> ≟ <span className="mono">{c.b}</span>
            <div className="muted small">{c.reason}</div>
          </span>
          <button className="primary" onClick={() => decide(c.id, 'accept')}>
            Same — merge
          </button>
          <button className="btn-secondary" onClick={() => decide(c.id, 'reject')}>
            Different
          </button>
        </div>
      ))}
    </Card>
  );
}

export default function Objects() {
  const schema = useApi<OntSchema>('/api/v1/ontology/schema');
  const [type, setType] = useState('');
  const [q, setQ] = useState('');
  const [sel, setSel] = useState(objectFromHash());
  const list = useApi<{ objects: OntObject[] }>(`/api/v1/ontology/objects?type=${encodeURIComponent(type)}&q=${encodeURIComponent(q)}`, 30000);

  useEffect(() => {
    const on = () => setSel(objectFromHash());
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);

  return (
    <>
      <PageHero
        eyebrow="Business"
        title="Objects"
        tint="purple"
        lede="The business objects Zyntra knows about, how they link, and where each fact came from. Links show what depends on what; they never feed the simulator."
      />
      <ErrorNote message={schema.error} />
      <Resolution />
      <div className="segmented" role="tablist">
        <button role="tab" aria-selected={type === ''} onClick={() => setType('')}>
          All
        </button>
        {schema.data?.objects.map((t) => (
          <button key={t.name} role="tab" aria-selected={type === t.name} onClick={() => setType(t.name)}>
            {t.name}
          </button>
        ))}
      </div>
      <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search by name or id" aria-label="Search objects" maxLength={100} />
      <div className="two-col">
        <Card>
          {list.data && list.data.objects.length === 0 ? (
            <Empty>No objects match.</Empty>
          ) : (
            <ul className="plain">
              {list.data?.objects.map((o) => (
                <li key={o.id}>
                  <button className={`linklike${sel === o.id ? ' active' : ''}`} onClick={() => openObject(o.id)}>
                    {nameOf(o)}
                  </button>{' '}
                  <span className="muted small">{o.type}</span>
                </li>
              ))}
            </ul>
          )}
        </Card>
        {sel ? <Detail id={sel} /> : <Card><Empty>Select an object to see its facts, links and dependents.</Empty></Card>}
      </div>
    </>
  );
}

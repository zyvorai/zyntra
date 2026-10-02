import { useState } from 'react';
import { api, type PackDraft } from '../api';
import { Card, ErrorNote, Pill } from './ui';

async function readFiles(list: FileList | null): Promise<{ name: string; content: string }[]> {
  if (!list) return [];
  return Promise.all(Array.from(list).map(async (f) => ({ name: f.name, content: await f.text() })));
}

function download(name: string, body: string) {
  const url = URL.createObjectURL(new Blob([body], { type: 'text/plain' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name.replaceAll('/', '_');
  a.click();
  URL.revokeObjectURL(url);
}

export default function PackDraftCard() {
  const [industry, setIndustry] = useState('');
  const [files, setFiles] = useState<FileList | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [res, setRes] = useState<PackDraft | null>(null);
  const [show, setShow] = useState('kpis.yaml');

  const run = async () => {
    setBusy(true);
    setErr('');
    try {
      const samples = await readFiles(files);
      setRes(await api<PackDraft>('/api/v1/ai/pack-draft', { method: 'POST', json: { industry, samples } }));
    } catch (e) {
      setErr(String((e as Error).message ?? e));
    } finally {
      setBusy(false);
    }
  };
  const valid = res?.validation && !res.validation.errors?.length;

  return (
    <Card title="Draft a pack from a sample" aside={res ? <Pill tone={res.mode === 'llm' ? 'purple' : 'neutral'}>{res.mode === 'llm' ? res.model : 'columns only'}</Pill> : null}>
      <p className="small muted">
        Drop a CSV or JSON export and name the site in one line. You get pack.yaml and kpis.yaml to edit; nothing is saved or loaded. An action whose effect cites no
        column in the sample is refused.
      </p>
      <div className="row-actions">
        <input aria-label="Industry" placeholder="kirana counter, batch plant, auth gateway…" value={industry} onChange={(e) => setIndustry(e.target.value)} />
        <input aria-label="Samples" type="file" multiple accept=".csv,.json,.yaml,.yml" onChange={(e) => setFiles(e.target.files)} />
        <button className="btn-primary" disabled={busy || !industry.trim() || !files?.length} onClick={run}>
          {busy ? 'Drafting…' : 'Draft'}
        </button>
      </div>
      <ErrorNote message={err} />
      {res ? (
        <>
          <p className="small">
            <Pill tone={valid ? 'ok' : 'bad'}>{valid ? 'pack validate: ok' : 'pack validate: errors'}</Pill>{' '}
            {res.validation?.ok.join(' · ')}
          </p>
          {res.validation?.errors?.length ? <ul className="small down">{res.validation.errors.map((x) => <li key={x}>{x}</li>)}</ul> : null}
          {res.notes?.map((n) => (
            <p key={n} className="small muted">
              {n}
            </p>
          ))}
          {res.refused?.length ? (
            <details className="trace" open>
              <summary>Refused ({res.refused.length})</summary>
              <ul className="small">{res.refused.map((r) => <li key={r}>{r}</li>)}</ul>
            </details>
          ) : null}
          <div className="pills">
            {Object.keys(res.files)
              .filter((f) => !f.startsWith('fixture/'))
              .map((f) => (
                <button key={f} className={f === show ? 'btn-primary' : 'btn-secondary'} onClick={() => setShow(f)}>
                  {f}
                </button>
              ))}
            <button className="btn-secondary" onClick={() => download(show, res.files[show] ?? '')}>
              Download {show}
            </button>
          </div>
          <pre className="code">{res.files[show]}</pre>
        </>
      ) : null}
    </Card>
  );
}

import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Sparkles } from 'lucide-react';
import { api, type AIStatus, type Answer, type Draft, type OntActionType, type Proposal } from '../api';
import { useApi } from '../hooks';
import { Card, PageHero, Pill } from '../components/ui';
import { openObject } from '../nav';

interface Turn { q: string; a?: Answer; error?: string }

const suggestions = [
  'What are the biggest gaps right now?',
  'What should we do first?',
  'Why is the plan recommending that?',
  'Any anomalies?',
  'What will breach next?',
  'Are all sources healthy?',
];

export default function Ask() {
  const st = useApi<AIStatus>('/api/v1/ai/status');
  const [turns, setTurns] = useState<Turn[]>([]);
  const [q, setQ] = useState('');
  const [busy, setBusy] = useState(false);
  const end = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    end.current?.scrollIntoView({ behavior: 'smooth', block: 'end' });
  }, [turns]);

  const ask = async (question: string) => {
    const text = question.trim();
    if (!text || busy) return;
    setQ('');
    setBusy(true);
    setTurns((t) => [...t, { q: text }]);
    try {
      const a = await api<Answer>('/api/v1/ai/ask', { method: 'POST', json: { question: text } });
      setTurns((t) => t.map((x, i) => (i === t.length - 1 ? { ...x, a } : x)));
    } catch (e) {
      setTurns((t) => t.map((x, i) => (i === t.length - 1 ? { ...x, error: (e as Error).message } : x)));
    } finally {
      setBusy(false);
    }
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    ask(q);
  };

  return (
    <>
      <PageHero
        eyebrow="Intelligence"
        title="Ask Zyntra"
        tint="purple"
        lede={
          <>
            Answers are grounded in the live model, gaps, plan, anomalies and forecasts
            {st.data?.mode === 'llm' ? ` and phrased by ${st.data.model || 'the Fabric AI gateway'}` : ''}. Zyntra AI is read-only — it never
            changes anything.
          </>
        }
      />
      <Card className="chat">
        {turns.length === 0 ? (
          <div className="suggestions">
            {suggestions.map((s) => (
              <button key={s} className="btn-secondary" onClick={() => ask(s)}>
                {s}
              </button>
            ))}
          </div>
        ) : (
          <div className="turns">
            {turns.map((t, i) => (
              <div key={i} className="turn">
                <p className="q">{t.q}</p>
                {t.a ? (
                  <div className="a">
                    <p className="prose">{t.a.text}</p>
                    <div className="pills">
                      <Pill tone={t.a.mode === 'llm' ? 'purple' : 'neutral'}>
                        <Sparkles size={12} aria-hidden /> {t.a.mode === 'llm' ? t.a.model || 'LLM' : 'grounded'}
                      </Pill>
                      <Pill>{t.a.intent}</Pill>
                      {t.a.llm_error ? <Pill tone="warn">LLM fallback</Pill> : null}
                    </div>
                    {t.a.citations?.length ? (
                      <details className="trace" open>
                        <summary>Cited facts ({t.a.citations.length})</summary>
                        <ul>
                          {t.a.citations.map((c, j) => (
                            <li key={j}>
                              <button className="linklike mono" onClick={() => openObject(c.object)}>
                                {c.object}.{c.property}
                              </button>{' '}
                              = {String(c.value)} <span className="muted small">from {c.source}, observed {new Date(c.observed_at).toLocaleString()}</span>
                            </li>
                          ))}
                        </ul>
                      </details>
                    ) : null}
                    {t.a.grounding?.length ? (
                      <details className="trace">
                        <summary>Grounding ({t.a.grounding.length})</summary>
                        <ul>
                          {t.a.grounding.map((g, j) => (
                            <li key={j}>{g}</li>
                          ))}
                        </ul>
                      </details>
                    ) : null}
                  </div>
                ) : t.error ? (
                  <p className="error-note">{t.error}</p>
                ) : (
                  <p className="muted">Thinking…</p>
                )}
              </div>
            ))}
            <div ref={end} />
          </div>
        )}
        <form className="ask-form" onSubmit={onSubmit}>
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Ask about gaps, plans, anomalies, forecasts…" maxLength={2000} autoFocus />
          <button className="primary" type="submit" disabled={busy || !q.trim()}>
            {busy ? 'Asking…' : 'Ask'}
          </button>
        </form>
      </Card>
      <DraftProposal />
    </>
  );
}

/** Drafts a typed proposal from plain text. It creates nothing until you submit. */
function DraftProposal() {
  const types = useApi<{ actions: OntActionType[] }>('/api/v1/ontology/actions');
  const [text, setText] = useState('');
  const [draft, setDraft] = useState<Draft | null>(null);
  const [note, setNote] = useState('');
  if (types.error || !types.data?.actions?.length) return null;
  const run = async () => {
    setNote('');
    setDraft(await api<Draft>('/api/v1/ai/propose', { method: 'POST', json: { text } }));
  };
  const submit = async () => {
    try {
      const p = await api<Proposal>('/api/v1/proposals', { method: 'POST', json: { action: draft!.action, inputs: draft!.inputs } });
      setNote(`Proposal ${p.id} created. It now waits for approval.`);
      setDraft(null);
    } catch (e) {
      setNote((e as Error).message);
    }
  };
  return (
    <Card title="Draft a proposal">
      <p className="muted small">
        Name an action and the objects it applies to, for example “raise-inference-priority for Inference API”. Zyntra reads out what you named and checks the contract; it never
        chooses an action for you.
      </p>
      <form className="ask-form" onSubmit={(e) => { e.preventDefault(); run(); }}>
        <input value={text} onChange={(e) => setText(e.target.value)} placeholder="action id or title, then the objects" maxLength={500} />
        <button className="btn-secondary" type="submit" disabled={!text.trim()}>
          Draft
        </button>
      </form>
      {draft ? (
        <div>
          <p className="mono small">
            {draft.action || '(no action recognised)'} {draft.inputs ? JSON.stringify(draft.inputs) : ''}
          </p>
          {draft.problems?.length ? (
            <ul className="blocked-list">
              {draft.problems.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          ) : null}
          <button className="primary" disabled={!draft.valid} onClick={submit}>
            Submit proposal
          </button>
        </div>
      ) : null}
      {note ? <p className="info-note">{note}</p> : null}
    </Card>
  );
}

import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Sparkles } from 'lucide-react';
import { api, type AIStatus, type Answer } from '../api';
import { useApi } from '../hooks';
import { Card, PageHero, Pill } from '../components/ui';

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
    </>
  );
}

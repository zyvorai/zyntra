import type { ReactNode } from 'react';
import { fmt, unitOf, type KPI } from '../api';

export function PageHero({
  eyebrow,
  title,
  lede,
  tint,
  actions,
}: {
  eyebrow?: string;
  title: string;
  lede?: ReactNode;
  tint?: 'green' | 'amber' | 'purple' | 'red';
  actions?: ReactNode;
}) {
  return (
    <header className={`page-hero${tint ? ` hero-tint-${tint}` : ''}`}>
      {eyebrow ? <p className="eyebrow">{eyebrow}</p> : null}
      <h1>{title}</h1>
      {lede ? <p>{lede}</p> : null}
      {actions ? <div className="hero-actions">{actions}</div> : null}
    </header>
  );
}

export function Card({ title, aside, children, className = '' }: { title?: ReactNode; aside?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={`card ${className}`.trim()}>
      {title || aside ? (
        <div className="card-head">
          {title ? <h2>{title}</h2> : <span />}
          {aside}
        </div>
      ) : null}
      {children}
    </section>
  );
}

export function Stat({ label, value, sub, tone }: { label: string; value: ReactNode; sub?: ReactNode; tone?: 'ok' | 'warn' | 'bad' | 'info' }) {
  return (
    <div className={`stat${tone ? ` stat-${tone}` : ''}`}>
      <span className="stat-label">{label}</span>
      <span className="stat-value">{value}</span>
      {sub ? <span className="stat-sub">{sub}</span> : null}
    </div>
  );
}

export function Pill({ tone = 'neutral', children }: { tone?: 'ok' | 'warn' | 'bad' | 'info' | 'neutral' | 'purple'; children: ReactNode }) {
  return <span className={`pill pill-${tone}`}>{children}</span>;
}

export const riskTone = (r?: string) => (r === 'high' ? 'bad' : r === 'medium' ? 'warn' : 'ok');

export function Empty({ children }: { children: ReactNode }) {
  return <p className="empty">{children}</p>;
}

export function ErrorNote({ message }: { message: string }) {
  if (!message) return null;
  return (
    <p className="error-note" role="alert">
      {message}
    </p>
  );
}

export function Sparkline({ points, target, width = 140, height = 36 }: { points: number[]; target?: number; width?: number; height?: number }) {
  if (points.length < 2) return <svg width={width} height={height} className="spark" aria-hidden />;
  const vals = target !== undefined ? [...points, target] : points;
  const min = Math.min(...vals);
  const max = Math.max(...vals);
  const span = max - min || 1;
  const x = (i: number) => (i / (points.length - 1)) * (width - 2) + 1;
  const y = (v: number) => height - 2 - ((v - min) / span) * (height - 4);
  const d = points.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join('');
  return (
    <svg width={width} height={height} className="spark" aria-hidden>
      {target !== undefined ? <line x1={0} x2={width} y1={y(target)} y2={y(target)} className="spark-target" /> : null}
      <path d={d} className="spark-line" />
    </svg>
  );
}

export function kpiMet(k: KPI): boolean | null {
  if (k.target === undefined || k.target === null) return null;
  return k.direction === 'lower' ? k.value <= k.target : k.value >= k.target;
}

export function KpiValue({ k }: { k: KPI }) {
  return (
    <>
      {fmt(k.value)}
      {unitOf(k) ? <small className="unit"> {unitOf(k)}</small> : null}
    </>
  );
}

export function Meter({ value }: { value: number }) {
  const w = Math.max(2, Math.min(100, value * 100));
  return (
    <span className="meter" aria-hidden>
      <span style={{ width: `${w}%` }} className={value > 0.5 ? 'bad' : value > 0.15 ? 'warn' : 'ok'} />
    </span>
  );
}

export function OwnerFilter({ owners, value, onChange }: { owners?: string[]; value: string; onChange: (o: string) => void }) {
  if (!owners?.length && !value) return null;
  return (
    <label className="owner-filter">
      <span className="muted small">Owner</span>
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Everyone</option>
        {(owners ?? []).map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
        {value && !owners?.includes(value) ? <option value={value}>{value}</option> : null}
      </select>
    </label>
  );
}

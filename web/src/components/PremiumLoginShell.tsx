// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Apple Store chapter login, ported from Zyvor Fabric: a full-bleed hero
// chapter (brand, instance strip, title, pills, CTAs) followed by a
// parchment credentials chapter.
import type { ReactNode } from 'react';
import { AlertCircle } from 'lucide-react';
import '../login.css';

export type PremiumLoginTone = 'sky' | 'violet' | 'emerald' | 'amber' | 'orange';

export type PremiumLoginPill = { icon?: ReactNode; label: string; tone?: PremiumLoginTone };

export function PremiumLoginShell({
  logo,
  productName,
  heroTitle,
  heroSubheadline,
  heroCta,
  accent = 'violet',
  chapterNote,
  pills,
  instanceMeta,
  panelSubtitle,
  panelHint,
  footer,
  children,
}: {
  logo?: ReactNode;
  productName: string;
  heroTitle: ReactNode;
  heroSubheadline?: ReactNode;
  heroCta?: ReactNode;
  accent?: PremiumLoginTone;
  chapterNote?: ReactNode;
  pills?: PremiumLoginPill[];
  instanceMeta?: ReactNode;
  panelSubtitle?: ReactNode;
  panelHint?: ReactNode;
  footer?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="login-page login-store-page" data-tone={accent}>
      <main className="login-store-scroll" aria-label="Sign in">
        <section className="login-chapter login-chapter-hero" data-tone={accent} aria-label={productName}>
          <div className="login-chapter-inner">
            {logo ? <div className="login-logo" style={{ marginBottom: '1.25rem' }}>{logo}</div> : null}
            <p className="login-wordmark">{productName}</p>
            {instanceMeta ? <div className="login-instance-meta">{instanceMeta}</div> : null}
            <h1 className="login-hero-title">{heroTitle}</h1>
            {heroSubheadline ? <p className="login-tagline">{heroSubheadline}</p> : null}
            {pills?.length ? (
              <div className="login-pill-row">
                {pills.map((pill) => (
                  <span key={pill.label} data-tone={pill.tone ?? accent} className="login-pill">
                    <span className="login-pill-dot" aria-hidden />
                    {pill.icon}
                    {pill.label}
                  </span>
                ))}
              </div>
            ) : null}
            {heroCta ? <div className="login-cta">{heroCta}</div> : null}
            {chapterNote ? <p className="login-chapter-note">{chapterNote}</p> : null}
          </div>
        </section>

        <section id="login-sign-in" className="login-chapter login-chapter-sign-in" aria-label="Credentials">
          <div className="login-chapter-inner login-sign-in-inner">
            <p className="login-form-heading">{panelSubtitle ?? 'Sign in'}</p>
            <div className="login-card">{children}</div>
            {panelHint ? <p className="login-hint">{panelHint}</p> : null}
          </div>
        </section>
      </main>
      {footer}
    </div>
  );
}

export function LoginError({ message }: { message: string }) {
  return (
    <div className="login-error login-shake" role="alert" aria-live="assertive">
      <AlertCircle size={16} aria-hidden />
      <div>
        <p className="login-error-title">Unable to sign in</p>
        <p className="login-error-body">{message}</p>
      </div>
    </div>
  );
}

export function LoginField({ label, id, children }: { label: string; id: string; children: ReactNode }) {
  return (
    <div className="login-field">
      <label htmlFor={id}>{label}</label>
      <div className="login-field-wrap">{children}</div>
    </div>
  );
}

export function LoginRemember({
  checked,
  onChange,
  label = 'Remember me on this device',
  hint,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label?: string;
  hint?: string;
}) {
  return (
    <div className="login-remember">
      <label>
        <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
        <span>{label}</span>
      </label>
      {hint ? <p>{hint}</p> : null}
    </div>
  );
}

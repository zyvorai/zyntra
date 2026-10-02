import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { ArrowRight, ChevronLeft, Eye, EyeOff, KeyRound, Loader2, LogIn, User } from 'lucide-react';
import { login, passwordLogin, type Meta } from '../api';
import {
  LoginError,
  LoginField,
  LoginRemember,
  PremiumLoginShell,
  type PremiumLoginPill,
} from '../components/PremiumLoginShell';

const SAVE_KEY = 'zyntra-login';

// autoFocus would scroll past the hero chapter on first paint.
const focusInPlace = (el: HTMLInputElement | null) => el?.focus({ preventScroll: true });

type Step = 'identify' | 'key';

function readSaved(): { operator: string } | null {
  try {
    const v = JSON.parse(localStorage.getItem(SAVE_KEY) || 'null');
    return v && typeof v.operator === 'string' ? v : null;
  } catch {
    return null;
  }
}

export default function Login({ meta, onSignedIn }: { meta: Meta | null; onSignedIn: () => void }) {
  const saved = readSaved();
  const [operator, setOperator] = useState(saved?.operator ?? '');
  const [step, setStep] = useState<Step>(saved?.operator ? 'key' : 'identify');
  const [token, setToken] = useState('');
  const [show, setShow] = useState(false);
  const [remember, setRemember] = useState(Boolean(saved));
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const methods = meta?.auth_methods ?? ['key'];
  const sso = methods.includes('oidc');
  const hasPassword = methods.includes('password');
  const hasKey = methods.includes('key');
  const [useKey, setUseKey] = useState(!hasPassword);
  useEffect(() => setUseKey(!hasPassword), [hasPassword]);
  const secretLabel = useKey ? 'Access key' : 'Password';

  useEffect(() => {
    document.title = 'Sign in · Zyntra';
  }, []);

  const pills = useMemo<PremiumLoginPill[]>(
    () => [
      { label: 'Netra eBPF', tone: 'sky' },
      { label: 'Gravia GPU', tone: 'emerald' },
      { label: 'Fabric Keep', tone: 'orange' },
      { label: 'Human-approved', tone: 'violet' },
    ],
    []
  );

  const scrollToForm = () =>
    document.getElementById('login-sign-in')?.scrollIntoView({ behavior: 'smooth', block: 'start' });

  const onContinue = (e: FormEvent) => {
    e.preventDefault();
    if (!operator.trim()) return;
    setError('');
    setStep('key');
  };

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!token) return;
    setSubmitting(true);
    setError('');
    try {
      if (useKey) await login(operator.trim(), token, remember);
      else await passwordLogin(operator.trim(), token, remember);
      if (remember) localStorage.setItem(SAVE_KEY, JSON.stringify({ operator: operator.trim() }));
      else localStorage.removeItem(SAVE_KEY);
      setToken('');
      onSignedIn();
    } catch (err) {
      const msg = (err as Error).message;
      setError(msg === 'invalid credentials' ? `That ${secretLabel.toLowerCase()} was not accepted.` : msg);
    } finally {
      setSubmitting(false);
    }
  };

  const instanceMeta = meta ? (
    <>
      <span className="login-instance-chip" data-kind="product">
        <strong>{meta.product}</strong>
      </span>
      {meta.host ? (
        <span className="login-instance-chip" title="Host">
          {meta.host}
        </span>
      ) : null}
      <span className="login-instance-chip" title="Model">
        {meta.model}
      </span>
      <span className="login-instance-chip" data-kind={meta.approval_mode === 'keep' ? 'k8s' : undefined} title="Approvals">
        {meta.approval_mode === 'keep' ? 'Keep-gated' : 'local approvals'} · {meta.execute_mode}
      </span>
      <span className="login-instance-chip" title="Version">
        v{meta.version}
      </span>
    </>
  ) : null;

  const formInstance = meta ? (
    <dl className="login-form-instance">
      <dt>Product</dt>
      <dd>{meta.product}</dd>
      <dt>System</dt>
      <dd>
        {meta.host || window.location.host} · {meta.model}
      </dd>
      <dt>Sources</dt>
      <dd>
        {meta.sources.healthy}/{meta.sources.total} healthy · AI {meta.ai_mode}
      </dd>
    </dl>
  ) : null;

  return (
    <PremiumLoginShell
      logo={
        <a href="https://zyvor.dev" target="_blank" rel="noopener noreferrer" className="login-zyvor-mark" aria-label="zyvor.dev">
          <img src="/zyvor-mark.svg" alt="" />
        </a>
      }
      productName="Zyvor Zyntra"
      heroTitle="Decide. Then act."
      heroSubheadline="Decision intelligence for infrastructure — eBPF signals from Netra, GPU state from Gravia, actions gated by Fabric Keep."
      accent="violet"
      pills={pills}
      instanceMeta={instanceMeta}
      heroCta={
        <>
          <a
            href="#login-sign-in"
            className="login-cta-primary"
            onClick={(e) => {
              e.preventDefault();
              scrollToForm();
            }}
          >
            Sign in
          </a>
          <a href="https://zyvor.dev" target="_blank" rel="noopener noreferrer" className="login-cta-secondary">
            Learn more
          </a>
        </>
      }
      chapterNote={meta ? `${meta.product} · ${meta.model} · ${meta.host || window.location.host}` : 'Zyvor Zyntra · sign in to continue'}
      panelSubtitle={
        step === 'key' ? (
          <>
            Enter the {secretLabel.toLowerCase()} for <span className="login-apple-host">{operator.trim()}</span>
          </>
        ) : (
          'Sign in to Zyntra'
        )
      }
      panelHint={
        step === 'identify' && sso ? (
          <>Sign in with your organisation account; your groups decide whether you can view, propose, approve or execute.</>
        ) : step === 'identify' && hasPassword ? (
          <>Use the account from the Zyntra policy file. Your name is recorded in the decision audit trail.</>
        ) : step === 'identify' ? (
          <>
            Your name is recorded in the approval audit trail. The access key is{' '}
            <span className="login-mono">ZYNTRA_API_KEY</span> from{' '}
            <span className="login-mono">/etc/zyntra/zyntra.env</span> (
            <span className="login-mono">sudo grep ZYNTRA_API_KEY /etc/zyntra/zyntra.env</span>).
          </>
        ) : null
      }
      footer={
        <p className="login-hint login-footer">
          © 2026 Zyvor ·{' '}
          <a href="https://zyvor.dev" target="_blank" rel="noopener noreferrer" className="login-zyvor-link">
            zyvor.dev
          </a>
        </p>
      }
    >
      {step === 'identify' ? (
        <form key="identify" onSubmit={onContinue} autoComplete="on" aria-label="Operator" className="login-apple-step" noValidate>
          {formInstance}
          {error ? <LoginError message={error} /> : null}
          {sso ? (
            <>
              <button type="button" className="login-btn-primary sso-btn" onClick={() => window.location.assign('/api/v1/auth/oidc/login')}>
                <LogIn size={16} />
                <span>Sign in with SSO</span>
              </button>
              {hasPassword || hasKey ? <p className="login-divider">or {hasPassword ? 'a local account' : 'the break-glass access key'}</p> : null}
            </>
          ) : null}
          <div className="login-apple-fields" hidden={sso && !hasPassword && !hasKey}>
            <LoginField label={hasPassword ? 'Username' : 'Operator'} id="operator">
              <User className="login-field-icon" />
              <input
                id="operator"
                name="username"
                type="text"
                value={operator}
                onChange={(e) => setOperator(e.target.value)}
                className="login-input"
                placeholder="your-name"
                autoComplete="username"
                ref={focusInPlace}
                required
                maxLength={64}
              />
            </LoginField>
          </div>
          <button type="submit" className={sso ? 'login-btn-secondary btn-secondary sso-btn' : 'login-btn-primary'} disabled={!operator.trim()} hidden={sso && !hasPassword && !hasKey}>
            <span>Continue</span>
            <ArrowRight size={16} />
          </button>
        </form>
      ) : (
        <form key="key" onSubmit={onSubmit} autoComplete="on" aria-label="Access key" className="login-apple-step" noValidate>
          <button type="button" onClick={() => setStep('identify')} className="login-apple-identity" aria-label="Change operator">
            <ChevronLeft size={16} aria-hidden />
            <span>{operator.trim()}</span>
          </button>
          <input type="text" name="username" value={operator} autoComplete="username" readOnly hidden />
          {formInstance}
          {error ? <LoginError message={error} /> : null}
          <div className="login-apple-fields">
            <LoginField label={secretLabel} id="token">
              <KeyRound className="login-field-icon" />
              <input
                id="token"
                name="password"
                type={show ? 'text' : 'password'}
                value={token}
                onChange={(e) => setToken(e.target.value)}
                className="login-input"
                style={{ paddingRight: '2.75rem' }}
                placeholder={secretLabel}
                autoComplete="current-password"
                ref={focusInPlace}
                required
                disabled={submitting}
              />
              <button
                type="button"
                className="login-field-toggle"
                onClick={() => setShow(!show)}
                aria-label={show ? `Hide ${secretLabel.toLowerCase()}` : `Show ${secretLabel.toLowerCase()}`}
              >
                {show ? <EyeOff size={16} /> : <Eye size={16} />}
              </button>
            </LoginField>
          </div>
          {hasPassword && hasKey ? (
            <button type="button" className="linklike small" onClick={() => setUseKey(!useKey)}>
              {useKey ? 'Use a password instead' : 'Use the break-glass access key instead'}
            </button>
          ) : null}
          <LoginRemember checked={remember} onChange={setRemember} hint="Keeps you signed in for 7 days instead of 12 hours." />
          <button type="submit" className="login-btn-primary" disabled={!token || submitting}>
            {submitting ? (
              <>
                <Loader2 size={16} className="login-spin" />
                <span>Signing in…</span>
              </>
            ) : (
              <>
                <span>Sign In</span>
                <ArrowRight size={16} />
              </>
            )}
          </button>
        </form>
      )}
    </PremiumLoginShell>
  );
}

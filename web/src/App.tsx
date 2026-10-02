import { useCallback, useEffect, useState } from 'react';
import { api, AUTH_EXPIRED, logout, type Meta, type WhoAmI } from './api';
import { usePulse } from './hooks';
import { pageFromHash, pageTitles, type Page } from './nav';
import { readStoredTheme, toggleTheme, type Theme } from './theme';
import Nav from './components/Nav';
import Login from './pages/Login';
import Overview from './pages/Overview';
import Gaps from './pages/Gaps';
import Plan from './pages/Plan';
import Simulate from './pages/Simulate';
import Approvals from './pages/Approvals';
import Audit from './pages/Audit';
import Signals from './pages/Signals';
import Insights from './pages/Insights';
import Ask from './pages/Ask';
import ModelPage from './pages/ModelPage';
import Decision from './pages/Decision';
import Objects from './pages/Objects';
import Workflows from './pages/Workflows';
import Scenarios from './pages/Scenarios';
import { WhoContext } from './session';

type Session = { state: 'loading' } | { state: 'anon' } | { state: 'in'; who: WhoAmI };

function Console({ who, meta, onLogout }: { who: WhoAmI; meta: Meta | null; onLogout?: () => void }) {
  const [page, setPageState] = useState<Page>(pageFromHash);
  const [theme, setTheme] = useState<Theme>(readStoredTheme);
  const pulse = usePulse();

  useEffect(() => {
    const onHash = () => setPageState(pageFromHash());
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, []);

  useEffect(() => {
    document.title = `${pageTitles[page]} · Zyntra`;
  }, [page]);

  const setPage = useCallback((p: Page) => {
    window.location.hash = `/${p}`;
    setPageState(p);
    window.scrollTo({ top: 0 });
  }, []);

  return (
    <WhoContext.Provider value={who}>
      <a href="#main" className="skip-link">
        Skip to content
      </a>
      <Nav
        page={page}
        setPage={setPage}
        theme={theme}
        onToggleTheme={() => setTheme(toggleTheme(theme))}
        onLogout={onLogout}
        pulse={pulse}
        operator={who.identity.subject}
        ontology={meta?.ontology}
      />
      <main id="main">
        {who.default_password ? (
          <div className="banner" role="alert">
            <span>
              <strong>{who.identity.subject}</strong> still uses the default password. Set <code>ZYNTRA_ADMIN_PASSWORD</code> or define users in the policy file,
              then restart Zyntra.
            </span>
          </div>
        ) : null}
        {page === 'overview' && <Overview pulse={pulse} setPage={setPage} />}
        {page === 'gaps' && <Gaps />}
        {page === 'plan' && <Plan setPage={setPage} />}
        {page === 'simulate' && <Simulate />}
        {page === 'approvals' && <Approvals />}
        {page === 'audit' && <Audit />}
        {page === 'signals' && <Signals />}
        {page === 'insights' && <Insights setPage={setPage} />}
        {page === 'ask' && <Ask />}
        {page === 'model' && <ModelPage />}
        {page === 'objects' && <Objects />}
        {page === 'workflows' && <Workflows setPage={setPage} />}
        {page === 'scenarios' && <Scenarios />}
        {page === 'decision' && <Decision />}
      </main>
      <footer className="app-footer">
        <span>
          Zyntra {meta?.version ? `v${meta.version}` : ''} · {meta?.host || window.location.host} · signed in as{' '}
          <strong>{who.identity.subject}</strong> ({(who.identity.roles?.length ? who.identity.roles : [who.identity.role]).join(', ')})
        </span>
        <span>
          {meta ? `${meta.approval_mode} approvals · ${meta.execute_mode} · AI ${meta.ai_mode}` : ''} ·{' '}
          <a href="https://zyvor.dev" target="_blank" rel="noopener noreferrer">
            zyvor.dev
          </a>
        </span>
      </footer>
    </WhoContext.Provider>
  );
}

export default function App() {
  const [session, setSession] = useState<Session>({ state: 'loading' });
  const [meta, setMeta] = useState<Meta | null>(null);

  const check = useCallback(async () => {
    try {
      setSession({ state: 'in', who: await api<WhoAmI>('/api/v1/whoami') });
    } catch {
      setSession({ state: 'anon' });
    }
  }, []);

  useEffect(() => {
    api<Meta>('/api/v1/meta').then(setMeta).catch(() => undefined);
    check();
    const onExpired = () => setSession({ state: 'anon' });
    window.addEventListener(AUTH_EXPIRED, onExpired);
    return () => window.removeEventListener(AUTH_EXPIRED, onExpired);
  }, [check]);

  if (session.state === 'loading') return <div className="boot" aria-busy="true" />;
  if (session.state === 'anon') return <Login meta={meta} onSignedIn={check} />;
  const authRequired = session.who.auth_required;
  return (
    <Console
      who={session.who}
      meta={meta}
      onLogout={
        authRequired
          ? async () => {
              await logout().catch(() => undefined);
              setSession({ state: 'anon' });
            }
          : undefined
      }
    />
  );
}

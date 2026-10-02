import { ago, type AuditEvent, type ChainStatus } from '../api';
import { useApi } from '../hooks';
import { openDecision } from '../nav';
import { Card, Empty, ErrorNote, PageHero, Pill } from '../components/ui';

interface KeepStatus {
  configured: boolean;
  approval_mode: string;
  double_approval?: boolean;
  status?: Record<string, unknown>;
  error?: string;
}

function rows(v: unknown): Record<string, unknown>[] {
  if (Array.isArray(v)) return v as Record<string, unknown>[];
  if (v && typeof v === 'object') {
    for (const k of ['events', 'entries', 'receipts', 'items', 'audit', 'approvals']) {
      const x = (v as Record<string, unknown>)[k];
      if (Array.isArray(x)) return x as Record<string, unknown>[];
    }
  }
  return [];
}

const str = (v: unknown) => (v === undefined || v === null ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v));

export default function Audit() {
  const local = useApi<{ events: AuditEvent[]; chain?: ChainStatus }>('/api/v1/audit', 10000);
  const chain = local.data?.chain;
  const keep = useApi<KeepStatus>('/api/v1/keep/status', 30000);
  const configured = keep.data?.configured;
  const keepAudit = useApi<unknown>(configured ? '/api/v1/keep/audit' : null, 15000);
  const events = [...(local.data?.events ?? [])].reverse();
  const kAudit = rows(keepAudit.data).slice(-50).reverse();

  return (
    <>
      <PageHero
        eyebrow="Act"
        title="Audit"
        lede="Every proposal transition in Zyntra, hash-chained so edits or deletions are detectable, alongside the audit chain kept by Fabric Keep."
      />
      <ErrorNote message={local.error} />
      <Card
        title="Zyntra decisions"
        aside={
          <div className="pills">
            {chain ? (
              <Pill tone={chain.ok ? 'ok' : 'bad'}>
                {chain.ok ? `chain intact · head ${chain.head.slice(0, 10)}` : `chain broken at #${chain.broken_at}: ${chain.error}`}
              </Pill>
            ) : null}
            <Pill>{events.length} events</Pill>
          </div>
        }
      >
        {events.length === 0 ? (
          <Empty>No decisions yet.</Empty>
        ) : (
          <table className="table compact">
            <thead>
              <tr>
                <th>When</th>
                <th>Proposal</th>
                <th>Action</th>
                <th>Transition</th>
                <th>By</th>
                <th>Note</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e, i) => (
                <tr key={i}>
                  <td title={e.at}>{ago(e.at)}</td>
                  <td className="mono">
                    <button className="linklike mono" onClick={() => openDecision(e.proposal)}>
                      {e.proposal}
                    </button>
                  </td>
                  <td className="mono">{e.action}</td>
                  <td>
                    {e.from ? `${e.from} → ` : ''}
                    <strong>{e.to}</strong>
                  </td>
                  <td>{e.by}</td>
                  <td className="muted">{e.note}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <Card
        title="Fabric Keep"
        aside={
          keep.data ? (
            <Pill tone={!configured ? 'neutral' : keep.data.error ? 'bad' : 'purple'}>
              {!configured ? 'not configured' : keep.data.error ? 'unreachable' : `${keep.data.approval_mode} mode`}
            </Pill>
          ) : null
        }
      >
        {!configured ? (
          <Empty>Set ZYNTRA_KEEP_URL and ZYNTRA_KEEP_TOKEN to mirror decisions into Keep's audit chain.</Empty>
        ) : keep.data?.error ? (
          <ErrorNote message={keep.data.error} />
        ) : kAudit.length === 0 ? (
          <Empty>{keepAudit.error || 'No Keep audit entries yet.'}</Empty>
        ) : (
          <table className="table compact">
            <thead>
              <tr>
                <th>When</th>
                <th>Event</th>
                <th>Session</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {kAudit.map((e, i) => (
                <tr key={i}>
                  <td>{ago(str(e.at ?? e.time ?? e.timestamp ?? e.created_at))}</td>
                  <td className="mono">{str(e.type ?? e.event ?? e.kind ?? e.action)}</td>
                  <td className="mono">{str(e.session_id ?? e.session)}</td>
                  <td className="muted truncate">{str(e.detail ?? e.message ?? e.data ?? e.payload)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </>
  );
}

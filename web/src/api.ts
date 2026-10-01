export type Direction = 'higher' | 'lower';
export type Risk = 'low' | 'medium' | 'high';

export interface Source {
  kind: string;
  query?: string;
  metric?: string;
  endpoint?: string;
  path?: string;
  field?: string;
  labels?: Record<string, string>;
  agg?: string;
  scale?: number;
  rate?: boolean;
}

export interface KPI {
  id: string;
  name: string;
  unit?: string;
  owner?: string;
  value: number;
  target?: number;
  direction?: Direction;
  source?: Source;
}

export interface Edge { from: string; to: string; weight: number; why?: string }
export interface Effect { kpi: string; change: number }
export interface Action {
  id: string;
  name: string;
  description?: string;
  adapter?: string;
  risk?: Risk;
  effects: Effect[];
  execute?: { template: string; params?: Record<string, string> };
}
export interface Model { name: string; kpis: KPI[]; edges: Edge[]; actions: Action[] }

export interface Gap {
  kpi: string;
  name: string;
  owner?: string;
  unit?: string;
  value: number;
  target: number;
  direction: Direction;
  severity: number;
}

export interface KPIResult {
  kpi: string;
  name: string;
  unit?: string;
  before: number;
  after: number;
  change: number;
  met_before: boolean;
  met_after: boolean;
  has_target: boolean;
  severity_before: number;
  severity_after: number;
}
export interface Step { from?: string; to: string; weight?: number; delta: number; text: string }
export interface SimResult {
  action: string;
  action_name: string;
  kpis: KPIResult[];
  trace: Step[];
  severity_before: number;
  severity_after: number;
  gaps_closed: string[] | null;
  gaps_opened: string[] | null;
}
export interface Recommendation {
  rank: number;
  action: string;
  name: string;
  adapter?: string;
  risk?: Risk;
  improvement: number;
  score: number;
  status: string;
  result: SimResult;
}

export interface SourceStatus {
  name: string;
  kind: string;
  ok: boolean;
  error?: string;
  latency_ms: number;
  kpis: string[];
  checked_at: string;
}

export interface Anomaly {
  kpi: string;
  name: string;
  value: number;
  mean: number;
  stddev: number;
  z: number;
  samples: number;
  severity: string;
  text: string;
}
export interface Forecast {
  kpi: string;
  name: string;
  status: 'breach' | 'at-risk' | 'improving' | 'stable';
  slope_per_hour: number;
  eta_seconds?: number;
  value: number;
  target: number;
  samples: number;
  text: string;
}
export interface Answer {
  text: string;
  intent: string;
  grounding: string[] | null;
  mode: 'heuristic' | 'llm';
  model?: string;
  llm_error?: string;
}
export interface AIStatus { mode: string; provider?: string; model?: string; mutations: string }

export type ProposalStatus = 'pending' | 'approved' | 'rejected' | 'executed' | 'failed';
export interface ExecResult { mode: string; args: string[]; output: string; ok: boolean; error?: string }
export interface KeepRef { mode: string; session_id?: string; approval_id?: string; receipt_id?: string; error?: string }
export interface Proposal {
  id: string;
  action: string;
  action_name: string;
  risk?: string;
  adapter?: string;
  template?: string;
  render?: string;
  render_error?: string;
  predicted: {
    severity_before: number;
    severity_after: number;
    closes?: string[];
    opens?: string[];
    kpis: Record<string, number>;
  };
  baseline: Record<string, number>;
  actual?: Record<string, number>;
  status: ProposalStatus;
  created_at: string;
  created_by: string;
  decided_at?: string;
  decided_by?: string;
  reason?: string;
  execution?: ExecResult;
  executed_at?: string;
  keep?: KeepRef;
}
export interface AuditEvent {
  at: string;
  proposal: string;
  action: string;
  from?: string;
  to: string;
  by: string;
  note?: string;
}

export interface Meta {
  product: string;
  version: string;
  host: string;
  model: string;
  auth_required: boolean;
  sources: { total: number; healthy: number };
  approval_mode: string;
  execute_mode: string;
  ai_mode: string;
}

export interface Pulse {
  at: string;
  severity_total: number;
  gaps: Gap[];
  top: Recommendation[];
  anomalies: number;
  sources: SourceStatus[] | null;
  pending_approvals: number;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export const AUTH_EXPIRED = 'zyntra:auth-expired';

export async function api<T>(path: string, init?: RequestInit & { json?: unknown }): Promise<T> {
  const headers = new Headers(init?.headers);
  let body = init?.body;
  if (init?.json !== undefined) {
    headers.set('Content-Type', 'application/json');
    body = JSON.stringify(init.json);
  }
  const res = await fetch(path, { ...init, headers, body, credentials: 'same-origin' });
  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    if (res.status === 401 && !path.endsWith('/session')) window.dispatchEvent(new Event(AUTH_EXPIRED));
    const msg = (data && typeof data === 'object' && 'error' in data ? String((data as { error: string }).error) : '') || res.statusText;
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export const login = (operator: string, token: string, remember: boolean) =>
  api<{ ok: boolean; operator?: string }>('/api/v1/session', { method: 'POST', json: { operator, token, remember } });

export interface WhoAmI {
  identity: { subject: string; role: string; method: string };
  auth_required: boolean;
}
export const logout = () => api('/api/v1/session', { method: 'DELETE' });

export function fmt(v: number, unit?: string): string {
  if (!Number.isFinite(v)) return '—';
  const a = Math.abs(v);
  const s = a >= 1000 ? v.toLocaleString(undefined, { maximumFractionDigits: 0 }) : a >= 100 ? v.toFixed(1) : a >= 1 ? v.toFixed(2) : v.toPrecision(3);
  return unit ? `${s.replace(/\.0+$|(\.\d*?)0+$/, '$1')} ${unit}` : s.replace(/\.0+$|(\.\d*?)0+$/, '$1');
}

export const pct = (v: number) => `${(v * 100).toFixed(1)}%`;

export const sev = (v: number) => v.toFixed(2);

export function ago(iso?: string): string {
  if (!iso) return '';
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `${Math.round(s)}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86400)}d ago`;
}

export function duration(sec: number): string {
  if (sec < 3600) return `${Math.round(sec / 60)} min`;
  if (sec < 86400) return `${(sec / 3600).toFixed(1)} h`;
  return `${(sec / 86400).toFixed(1)} days`;
}

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
  file?: string;
  url?: string;
  format?: string;
  name?: string;
  where?: Record<string, string>;
  denominator?: string;
  stale_after?: string;
}

export interface KPI {
  id: string;
  name: string;
  unit?: string;
  owner?: string;
  value: number;
  target?: number;
  direction?: Direction;
  criticality?: 'critical' | 'high' | 'normal' | 'low';
  min?: number;
  max?: number;
  source?: Source;
  unit_class?: string;
  currency?: string;
  calendar?: string;
}

export const unitOf = (k: { unit?: string; currency?: string }) => k.currency || k.unit;

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
  window?: string;
  approvers?: number;
  compensate?: string;
  preconditions?: { kpi: string; worse_than?: number; better_than?: number; why?: string }[];
  invariants?: { kpi: string; max_worsen: number; why?: string }[];
  webhook?: { method?: string; url: string };
  file?: { path: string };
}
export interface PackInfo { id: string; title?: string; industry?: string; version?: string; owners?: string[] }
export interface Model { name: string; kpis: KPI[]; edges: Edge[]; actions: Action[]; pack?: PackInfo; timezone?: string; calendars?: Record<string, { days?: string[]; start: string; end: string }> }

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
  low?: number;
  high?: number;
  settles_after?: string;
  bounded?: boolean;
  stale?: boolean;
}
export interface Violation { kpi: string; value: number; limit: number; kind: string; implicit?: boolean; text: string }
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
  actions?: string[];
  weighted_before?: number;
  weighted_after?: number;
  weighted_after_best?: number;
  weighted_after_worst?: number;
  violations?: Violation[];
  precondition_failures?: string[];
  stale_inputs?: string[];
  settles_after?: string;
}
export interface Recommendation {
  rank: number;
  action: string;
  actions?: string[];
  name: string;
  adapter?: string;
  risk?: Risk;
  improvement: number;
  weighted_improvement: number;
  pessimistic_improvement?: number;
  optimistic_only?: boolean;
  cancels?: string[];
  uncertainty: number;
  score: number;
  confidence: 'high' | 'medium' | 'low';
  stale_inputs?: string[];
  blocked_reasons?: string[];
  precondition_failures?: string[];
  settles_after?: string;
  status: string;
  result: SimResult;
}
export interface PlanResponse {
  recommendations: Recommendation[];
  blocked: Recommendation[];
  unusable_inputs: string[];
  model_version: string;
  owners?: string[];
}

export interface SourceStatus {
  name: string;
  kind: string;
  ok: boolean;
  state?: 'ok' | 'stale' | 'error' | 'fallback';
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
  citations?: Citation[];
  mode: 'heuristic' | 'llm';
  model?: string;
  llm_error?: string;
}
export interface AIStatus { mode: string; provider?: string; model?: string; mutations: string }

export type ProposalStatus = 'pending' | 'approved' | 'rejected' | 'expired' | 'blocked' | 'executed' | 'failed';
export type Phase =
  | 'proposed'
  | 'approved'
  | 'rejected'
  | 'expired'
  | 'blocked'
  | 'failed'
  | 'dry-run-validated'
  | 'applied'
  | 'observing'
  | 'verified'
  | 'regressed'
  | 'missed'
  | 'inconclusive'
  | 'rollback-proposed'
  | 'rolled-back';
export interface FreshnessState {
  kpi: string;
  status: 'fresh' | 'stale' | 'missing' | 'static' | 'held';
  last_success?: string;
  last_error?: string;
  age_seconds?: number;
  max_age?: string;
  required?: boolean;
}
export interface Approval { by: string; role?: string; method?: string; at: string; reason?: string }
export interface Alternative {
  action: string;
  name: string;
  score: number;
  weighted_improvement: number;
  confidence?: string;
  blocked_reasons?: string[];
}
export interface Revalidation {
  at: string;
  ok: boolean;
  reasons?: string[];
  model_version?: string;
  weighted_improvement: number;
  drift: number;
  stale_inputs?: string[];
}
export interface EffectivePolicy {
  rules?: string[];
  approvals: number;
  distinct_from_proposer: boolean;
  pending_expiry: string;
  approved_expiry: string;
  maintenance_windows?: string[];
  require_fresh: boolean;
  keep: string;
}
export interface Criterion { kpi: string; op: string; value?: number }
export interface OutcomeSample { at: string; values: Record<string, number>; stale?: string[]; met: boolean; breaches?: string[] }
export interface OutcomeRecord {
  state: 'observing' | 'verified' | 'regressed' | 'missed' | 'inconclusive';
  started_at: string;
  until: string;
  window: string;
  required_samples: number;
  tolerance: number;
  success_criteria: Criterion[];
  guardrails?: string[];
  baseline: Record<string, number>;
  samples: OutcomeSample[];
  reasons?: string[];
  decided_at?: string;
  predicted?: Record<string, number>;
  accuracy?: { kpi: string; baseline: number; predicted: number; actual: number; abs_error: number; hit: boolean }[];
}
export interface ExecResult {
  mode: string;
  kind?: string;
  args: string[];
  output: string;
  ok: boolean;
  error?: string;
  status?: number;
  response_hash?: string;
  written?: string;
  payload_hash?: string;
}
export interface KeepRef { mode: string; session_id?: string; approval_id?: string; receipt_id?: string; error?: string }
export interface Proposal {
  id: string;
  objects?: { input: string; id: string; type: string }[];
  object_digest?: string;
  object_outcome?: { checked_at: string; safe: boolean; still_at_risk?: ScenarioRisk[] };
  rollout?: { stages: { name: string; sites: string[] }[] };
  action: string;
  action_name: string;
  risk?: string;
  adapter?: string;
  template?: string;
  render?: string;
  render_error?: string;
  kinds?: string[];
  compensate?: string[];
  pack?: string;
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
  phase: Phase;
  created_at: string;
  created_by: string;
  expires_at?: string;
  approvals: Approval[];
  required_approvals: number;
  decided_at?: string;
  decided_by?: string;
  reason?: string;
  actions?: string[];
  model_version?: string;
  inputs?: { at: string; values: Record<string, number>; freshness?: FreshnessState[]; sources?: SourceStatus[] };
  simulation?: SimResult;
  alternatives?: Alternative[];
  policy?: EffectivePolicy;
  waiting_for_window?: boolean;
  revalidation?: Revalidation;
  blocked_reasons?: string[];
  execution?: ExecResult;
  executed_at?: string;
  outcome?: OutcomeRecord;
  keep?: KeepRef;
  rollback_of?: string;
  rollback_id?: string;
  evidence?: Record<string, Evidence>;
  explanation?: Explanation;
}
export interface EvidenceRow { source: string; index: number; values: Record<string, unknown> }
export interface Evidence {
  rows?: Record<string, EvidenceRow[]>;
  fills?: Record<string, { column: string; values: unknown[]; kpis: string[]; by: 'column' | 'model' }>;
}
export interface Finding {
  kpi: string;
  kind: string;
  edge?: string;
  weight?: number;
  predicted_change: number;
  actual_change: number;
  suggested_weight?: number;
  text: string;
}
export interface Explanation { proposal: string; verdict: string; hit_rate?: number; findings: Finding[]; text: string; grounding: string[]; hash: string }
export interface ExplanationResponse { explanation: Explanation; stored: boolean; verified: boolean; narrative: string; mode: string; model?: string; llm_error?: string }
export interface Precedent { proposal: string; action: string; name: string; at: string; score: number; overlap: string[]; result: string; hit_rate?: number; text: string }
export interface Precedents { items: Precedent[]; text: string }
export interface EdgeProposal {
  from: string;
  to: string;
  direction: 'leads' | 'together';
  r: number;
  n: number;
  weight: number;
  weight_low: number;
  weight_high: number;
  why: string;
  status: string;
  yaml: string;
}
export interface Contradiction { action: string; rule: { line: number; text: string; action?: string }; why: string; by: 'rule' | 'model' }
export interface PackDraft {
  files: Record<string, string>;
  refused?: string[];
  notes?: string[];
  mode: string;
  model?: string;
  llm_error?: string;
  validation?: { ok: string[]; warnings?: string[]; errors?: string[] };
}
export interface AuditEvent {
  seq?: number;
  at: string;
  proposal: string;
  action: string;
  from?: string;
  to: string;
  phase?: string;
  by: string;
  note?: string;
  payload_sha256?: string;
  response_sha256?: string;
  explanation_sha256?: string;
  prev_hash?: string;
  hash?: string;
}
export interface ChainStatus { ok: boolean; events: number; head: string; broken_at?: number; error?: string; migrated?: boolean }

export interface Meta {
  product: string;
  version: string;
  host: string;
  model: string;
  pack?: PackInfo | null;
  auth_required: boolean;
  auth_methods?: string[];
  sources: { total: number; healthy: number };
  approval_mode: string;
  execute_mode: string;
  ai_mode: string;
  ontology?: boolean;
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

export const passwordLogin = (username: string, password: string, remember: boolean) =>
  api<{ ok: boolean; operator?: string }>('/api/v1/session', { method: 'POST', json: { username, password, remember } });

export type Role = 'viewer' | 'proposer' | 'approver' | 'executor' | 'admin' | 'exec';
export interface WhoAmI {
  identity: { subject: string; role: Role; roles?: Role[]; method: string; tenant?: string };
  auth_required: boolean;
  methods?: string[];
  default_password?: boolean;
}

const capabilities: Record<'propose' | 'approve' | 'execute', Role[]> = {
  propose: ['proposer', 'approver', 'admin'],
  approve: ['approver', 'admin'],
  execute: ['executor', 'admin'],
};

export function can(who: WhoAmI | null, what: keyof typeof capabilities): boolean {
  if (!who) return false;
  const roles = who.identity.roles?.length ? who.identity.roles : [who.identity.role];
  return roles.some((r) => capabilities[what].includes(r));
}

export function until(iso?: string): string {
  if (!iso) return '';
  const s = (new Date(iso).getTime() - Date.now()) / 1000;
  if (s <= 0) return 'expired';
  if (s < 3600) return `${Math.ceil(s / 60)}m left`;
  if (s < 86400) return `${(s / 3600).toFixed(1)}h left`;
  return `${(s / 86400).toFixed(1)}d left`;
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

export interface ManualInput {
  kpi: string;
  name: string;
  unit?: string;
  owner?: string;
  value: number;
  entry?: { value: number; at: string; by: string; reason?: string };
}
export interface WebhookChannel { name: string; kpis: string[]; received_at?: string; from?: string }
export interface InputsResponse { manual: ManualInput[]; webhooks: WebhookChannel[] }

export const setManual = (kpi: string, value: number, reason: string) =>
  api<{ kpi: string }>(`/api/v1/kpis/${encodeURIComponent(kpi)}/value`, { method: 'POST', json: { value, reason } });

/** renderLabel names what a proposal will do when it runs. */
export function renderLabel(kinds?: string[], template?: string): string {
  const k = kinds?.length ? kinds : template ? ['kubectl'] : [];
  if (!k.length) return 'Advisory (nothing to run)';
  const names: Record<string, string> = { kubectl: 'Gravia resource (kubectl)', webhook: 'Webhook request', file: 'File to write', noop: 'Done by people (no system call)' };
  return Array.from(new Set(k)).map((x) => names[x] ?? x).join(' + ');
}

// Business ontology.

export interface Prov { source: string; source_id?: string; observed_at: string; ingested_at: string; transform?: string[] }
export interface OntObject {
  id: string;
  type: string;
  tenant?: string;
  props: Record<string, { v: string | number | boolean; prov: Prov }>;
  aliases?: { system: string; external_id: string }[];
}
export interface OntLink { id: string; type: string; from: string; to: string; prov: Prov }
export interface OntImpact { object: OntObject; depth: number; via: string }
export interface OntObjectDetail {
  object: OntObject;
  links: { link: OntLink; other: OntObject; out: boolean }[];
  impact: OntImpact[];
  bound_kpis: string[] | null;
  failing_kpis: string[] | null;
}
export interface OntInput { name: string; object_type: string; required?: boolean }
export interface OntActionType { id: string; inputs?: OntInput[]; affects?: string[]; permissions?: string[] }
export interface OntViewSpec { id: string; title: string; type: string; columns: string[]; actions?: string[]; exposed?: boolean }
export interface OntSchema {
  name?: string;
  objects: { name: string; properties: { name: string; type: string; sensitive?: boolean }[]; kpis?: string[] }[];
  links: { name: string; from: string; to: string }[];
  actions?: OntActionType[];
  views?: OntViewSpec[];
}
export interface OntViewRow { id: string; cells: Record<string, string | number | boolean>; failing_kpis?: string[]; exposed_by?: string[] }
export interface Candidate { id: string; a: string; b: string; reason: string; status: string; decided_by?: string }
export interface Citation { object: string; property: string; value: string | number | boolean; source: string; observed_at: string }
export interface Draft { action?: string; inputs?: Record<string, string>; valid: boolean; problems?: string[] }

export interface ScenarioRisk { id: string; type: string; kpis: string[] }
export interface Scenario {
  id: string;
  name: string;
  actions: string[];
  assumptions?: Record<string, number>;
  notes?: string;
  created_by: string;
  created_at: string;
  model_version?: string;
  data_version?: string;
  ran_at?: string;
  result?: {
    kpis: { kpi: string; name: string; before: number; after: number; met_after: boolean; has_target: boolean }[];
    weighted_before: number;
    weighted_after: number;
    gaps_closed: string[] | null;
    gaps_opened: string[] | null;
    blocked?: string[];
    at_risk_before: ScenarioRisk[] | null;
    at_risk_after: ScenarioRisk[] | null;
    exposed_before: { id: string; type: string }[] | null;
    exposed_after: { id: string; type: string }[] | null;
  };
}
export interface Comparison {
  scenarios: string[];
  rows: { kpi: string; name: string; unit?: string; before: number; after: number[]; met: boolean[] }[];
  weighted_after: number[];
  objects_at_risk_after: number[];
  objects_exposed_after: number[];
}
export interface OntChange {
  object: string;
  property: string;
  before?: { v: string | number | boolean; prov: Prov };
  after: { v: string | number | boolean; prov: Prov };
}

export interface ConnectorStatus {
  name: string;
  kind: string;
  interval: string;
  last_run?: string;
  last_success?: string;
  next_run?: string;
  last_error?: string;
  last_objects: number;
  last_links: number;
  last_skipped: number;
  duration_ms: number;
  runs: number;
  failures: number;
  streak: number;
  running: boolean;
  healthy: boolean;
}

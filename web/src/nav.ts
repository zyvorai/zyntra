export type Page =
  | 'overview'
  | 'gaps'
  | 'simulate'
  | 'plan'
  | 'approvals'
  | 'audit'
  | 'signals'
  | 'insights'
  | 'ask'
  | 'model'
  | 'objects'
  | 'workflows'
  | 'scenarios'
  | 'decision';

export interface NavChild { page: Page; label: string; blurb: string }
export interface NavGroup { label: string; page?: Page; children?: NavChild[]; ontology?: boolean }

export const navGroups: NavGroup[] = [
  { label: 'Overview', page: 'overview' },
  {
    label: 'Decide',
    children: [
      { page: 'gaps', label: 'Gaps', blurb: 'KPIs off target, ranked by relative shortfall.' },
      { page: 'plan', label: 'Plan', blurb: 'Actions ranked by improvement minus risk.' },
      { page: 'simulate', label: 'Simulate', blurb: 'What-if propagation through the KPI graph.' },
    ],
  },
  {
    label: 'Act',
    children: [
      { page: 'approvals', label: 'Approvals', blurb: 'Quorum approvals, revalidation and outcomes for each proposal.' },
      { page: 'audit', label: 'Audit', blurb: 'Hash-chained decision trail, plus Fabric Keep receipts.' },
    ],
  },
  {
    label: 'Business',
    ontology: true,
    children: [
      { page: 'objects', label: 'Objects', blurb: 'Business objects, links and where each fact came from.' },
      { page: 'workflows', label: 'Workflows', blurb: 'What is exposed to a failing KPI, and the typed actions to fix it.' },
      { page: 'scenarios', label: 'Scenarios', blurb: 'Saved what-if plans compared side by side.' },
    ],
  },
  { label: 'Signals', page: 'signals' },
  {
    label: 'Intelligence',
    children: [
      { page: 'insights', label: 'Insights', blurb: 'Anomalies and time-to-breach forecasts.' },
      { page: 'ask', label: 'Ask Zyntra', blurb: 'Grounded answers about gaps, plans and trends.' },
    ],
  },
  { label: 'Model', page: 'model' },
];

export const pageTitles: Record<Page, string> = {
  overview: 'Overview',
  gaps: 'Gaps',
  simulate: 'Simulate',
  plan: 'Plan',
  approvals: 'Approvals',
  audit: 'Audit',
  signals: 'Signals',
  insights: 'Insights',
  ask: 'Ask Zyntra',
  model: 'Model',
  objects: 'Objects',
  workflows: 'Workflows',
  scenarios: 'Scenarios',
  decision: 'Decision',
};

export function pageFromHash(): Page {
  const h = window.location.hash.replace(/^#\/?/, '').split('/')[0] as Page;
  return h in pageTitles ? h : 'overview';
}

/** The proposal id in #/decision/<id>. */
export function decisionFromHash(): string {
  const [page, id] = window.location.hash.replace(/^#\/?/, '').split('/');
  return page === 'decision' && id ? decodeURIComponent(id) : '';
}

export const openDecision = (id: string) => {
  window.location.hash = `/decision/${encodeURIComponent(id)}`;
};

/** The object id in #/objects/<id>. */
export function objectFromHash(): string {
  const [page, id] = window.location.hash.replace(/^#\/?/, '').split('/');
  return page === 'objects' && id ? decodeURIComponent(id) : '';
}

export const openObject = (id: string) => {
  window.location.hash = `/objects/${encodeURIComponent(id)}`;
};

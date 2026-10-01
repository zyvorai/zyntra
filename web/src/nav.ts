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
  | 'model';

export interface NavChild { page: Page; label: string; blurb: string }
export interface NavGroup { label: string; page?: Page; children?: NavChild[] }

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
      { page: 'approvals', label: 'Approvals', blurb: 'Approve or reject proposals; execute Gravia CRDs.' },
      { page: 'audit', label: 'Audit', blurb: 'Every decision and execution, plus Fabric Keep receipts.' },
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
};

export function pageFromHash(): Page {
  const h = window.location.hash.replace(/^#\/?/, '') as Page;
  return h in pageTitles ? h : 'overview';
}

/**
 * Shows an ISO 8601 timestamp (2026-10-04T17:00:00Z) in the viewer's locale and
 * leaves every other value exactly as it is, so ids, names and plain dates are
 * never rewritten. Property values arrive as raw JSON, so times are the one
 * kind the console has to recognise by shape.
 */
const ISO = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})$/;

export function display(v: unknown): string {
  if (typeof v === 'string' && ISO.test(v)) {
    const d = new Date(v);
    if (!Number.isNaN(d.getTime())) return d.toLocaleString();
  }
  return String(v);
}

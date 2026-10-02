/**
 * The accessible name of a control, the way a screen reader would compute the
 * common cases: aria-label, aria-labelledby, an associated or wrapping label,
 * visible text, a title, or an image alt inside. A placeholder does not count:
 * it disappears as soon as someone types.
 */
export function accessibleName(el: Element): string {
  const aria = el.getAttribute('aria-label')?.trim();
  if (aria) return aria;
  const by = el.getAttribute('aria-labelledby');
  if (by) {
    const t = by
      .split(' ')
      .map((id) => document.getElementById(id)?.textContent ?? '')
      .join(' ')
      .trim();
    if (t) return t;
  }
  if (el.id) {
    const l = document.querySelector(`label[for="${CSS.escape(el.id)}"]`);
    if (l?.textContent?.trim()) return l.textContent.trim();
  }
  const wrap = el.closest('label');
  if (wrap?.textContent?.trim()) return wrap.textContent.trim();
  const text = el.textContent?.trim();
  if (text) return text;
  const title = el.getAttribute('title')?.trim();
  if (title) return title;
  return el.querySelector('img[alt]')?.getAttribute('alt')?.trim() ?? '';
}

/** Every button, link, tab and form field in the container, by tag, that has no accessible name. */
export function unnamedControls(root: ParentNode = document): string[] {
  const out: string[] = [];
  root.querySelectorAll('button, a[href], [role=button], [role=tab], input:not([type=hidden]), select, textarea').forEach((el) => {
    if (!accessibleName(el)) out.push(el.outerHTML.slice(0, 120));
  });
  return out;
}

// Render sheets.html: one JPEG-ready PNG per sheet (1.5x) and one single-page PDF per sheet.
// Uses Playwright (PLAYWRIGHT=/path/to/node_modules/playwright, else web/node_modules or a
// sibling netra checkout) with the installed Google Chrome.
//   node docs/sales/enterprise-pricing/build.cjs <outdir>
const path = require('node:path');
const fs = require('node:fs');

const root = path.join(__dirname, '..', '..', '..');
const candidates = [
  process.env.PLAYWRIGHT,
  path.join(root, 'web', 'node_modules', 'playwright'),
  path.join(root, '..', 'netra', 'web', 'node_modules', 'playwright'),
].filter(Boolean);
const pw = candidates.find((p) => fs.existsSync(p));
if (!pw) {
  console.error('playwright not found; set PLAYWRIGHT=/path/to/node_modules/playwright');
  process.exit(1);
}
const { chromium } = require(pw);

const out = path.resolve(process.argv[2] || path.join(__dirname, '_out'));
fs.mkdirSync(out, { recursive: true });
const names = ['01-packaging', '02-launch-scope', '03-edition-pricing', '04-services-support', '05-ship-plan'];

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  });
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1.5 });
  const page = await ctx.newPage();
  await page.goto('file://' + path.join(__dirname, 'sheets.html'));
  await page.evaluate(() => document.fonts.ready);
  for (let i = 0; i < names.length; i++) {
    const id = '#s0' + (i + 1);
    const el = page.locator(id);
    const h = await el.evaluate((e) => Math.ceil(e.getBoundingClientRect().height));
    await el.screenshot({ path: path.join(out, names[i] + '.png') });
    await page.evaluate((keep) => document.querySelectorAll('.sheet').forEach((s) => { s.style.display = '#' + s.id === keep ? '' : 'none'; }), id);
    await page.pdf({ path: path.join(out, names[i] + '.pdf'), width: '1440px', height: h + 'px', printBackground: true, margin: { top: 0, right: 0, bottom: 0, left: 0 } });
    await page.evaluate(() => document.querySelectorAll('.sheet').forEach((s) => { s.style.display = ''; }));
    console.log(names[i], '1440 x', h);
  }
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });

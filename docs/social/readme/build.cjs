// Render the README cards and console frames to 3200 px JPEGs in docs/ux.
//   node docs/social/readme/build.cjs [card ...]
// Cards are 1600 CSS px wide, rendered at deviceScaleFactor 2, then converted with sips (macOS)
// or ImageMagick to JPEG quality 90. Playwright: PLAYWRIGHT=/path/to/node_modules/playwright.
const path = require('node:path');
const fs = require('node:fs');
const { execFileSync } = require('node:child_process');

const root = path.join(__dirname, '..', '..', '..');
const pw = [process.env.PLAYWRIGHT, path.join(root, 'web', 'node_modules', 'playwright')].filter(Boolean).find((p) => fs.existsSync(p));
if (!pw) { console.error('playwright not found; set PLAYWRIGHT=/path/to/node_modules/playwright'); process.exit(1); }
const { chromium } = require(pw);

const shots = {
  overview: ['Overview', 'Three KPIs off target. <em>One</em> ranked answer.', 'Live signals, the gaps they open and the best next actions on one screen, with a plain-language digest.'],
  plan: ['Plan', 'Every action, ranked <em>with the reason.</em>', 'Weighted improvement at the pessimistic end of each band, minus risk. Combined actions are scored together.'],
  simulate: ['Simulate', 'See the change <em>before</em> it happens.', 'Before, after, range and target for each KPI, with the propagation trace one click away.'],
  approvals: ['Approvals', 'Nothing runs until a person <em>says so.</em>', 'Baseline against prediction, the exact change, and a reason that lands in the audit trail.'],
  signals: ['Signals', 'Live sources, trend and target status.', 'Every value shows where it came from, how fresh it is and whether it meets target.'],
  model: ['Model', 'The KPI graph you can <em>read.</em>', 'Targets, owners and the cause-and-effect edges you declared. Predictions come only from these.'],
  objects: ['Objects', 'What depends on <em>what.</em>', 'A failing service, the lines it inspects and the orders behind them, with provenance on every fact.'],
  workflows: ['Workflows', 'The orders a failing KPI puts at <em>risk.</em>', 'Business objects exposed to a failing KPI, with the typed actions you can propose for them.'],
  scenarios: ['Scenarios', 'Compare plans <em>side by side.</em>', 'Save a plan with its assumptions and run it against current data. Running a scenario never changes anything.'],
};
const all = ['loop', 'capabilities', 'ontology', 'packs', 'safety', 'tenants-rollouts', 'deploy', 'editions'];
const names = process.argv.slice(2).length ? process.argv.slice(2) : [...all, ...Object.keys(shots).map((k) => 'shot-' + k)];
const out = path.join(root, 'docs', 'ux');
const tmp = fs.mkdtempSync(path.join(require('node:os').tmpdir(), 'zyntra-cards-'));

(async () => {
  const browser = await chromium.launch({ headless: true, channel: process.env.CHROME ? undefined : 'chrome', executablePath: process.env.CHROME });
  const ctx = await browser.newContext({ viewport: { width: 1600, height: 900 }, deviceScaleFactor: 2 });
  for (const n of names) {
    const page = await ctx.newPage();
    const shot = n.startsWith('shot-') ? shots[n.slice(5)] : null;
    const url = shot
      ? 'file://' + path.join(__dirname, 'frame.html') + '?' + new URLSearchParams({ img: '_captures/' + n.slice(5) + '.png', label: shot[0], title: shot[1], sub: shot[2] })
      : 'file://' + path.join(__dirname, n + '.html');
    await page.goto(url);
    await page.waitForFunction(() => !document.getElementById('img') || document.getElementById('img').complete);
    await page.evaluate(() => document.fonts.ready);
    const h = await page.evaluate(() => document.body.scrollHeight);
    const png = path.join(tmp, n + '.png');
    await page.screenshot({ path: png, fullPage: true });
    const jpg = path.join(out, (shot ? '' : 'readme-') + n + '.jpg');
    execFileSync('sips', ['-s', 'format', 'jpeg', '-s', 'formatOptions', '90', png, '--out', jpg], { stdio: 'ignore' });
    console.log(path.basename(jpg), 1600 * 2 + 'x' + h * 2, Math.round(fs.statSync(jpg).size / 1024) + ' KB');
    await page.close();
  }
  await browser.close();
  fs.rmSync(tmp, { recursive: true, force: true });
})();

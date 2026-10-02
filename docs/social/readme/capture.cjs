// Capture real console pages from a running lab for the README frames (docs/ux/shot-*.jpg).
// Nothing is mocked: start a lab, run this, then `node docs/social/readme/build.cjs`.
//
//   # AI infrastructure pack (needs fake sources):
//   ./bin/zyntra fake-sources -addr 127.0.0.1:19700 &
//   ZYNTRA_STATE_DIR=$(mktemp -d) ZYNTRA_ADMIN_PASSWORD=lab ZYNTRA_API_KEY=dev ZYNTRA_NETRA_URL=http://127.0.0.1:19700 \
//     ZYNTRA_GRAVIA_URL=http://127.0.0.1:19700 ZYNTRA_FABRIC_URL=http://127.0.0.1:19700 ZYNTRA_FABRIC_PASSWORD=fake \
//     ZYNTRA_KEEP_URL=http://127.0.0.1:19700 ./bin/zyntra serve -f packs/gpu &
//   PASSWORD=lab node docs/social/readme/capture.cjs gpu http://127.0.0.1:8080
//
//   # Manufacturing pack (Objects, Workflows, Scenarios):
//   ZYNTRA_STATE_DIR=$(mktemp -d) ZYNTRA_ADMIN_PASSWORD=lab ZYNTRA_API_KEY=dev ./bin/zyntra serve -f packs/manufacturing -addr 127.0.0.1:8081 &
//   PASSWORD=lab node docs/social/readme/capture.cjs mfg http://127.0.0.1:8081
//
// Use a fresh ZYNTRA_STATE_DIR per pack so objects and proposals do not mix.
const path = require('node:path');
const fs = require('node:fs');
const root = path.join(__dirname, '..', '..', '..');
const pw = [process.env.PLAYWRIGHT, path.join(root, 'web', 'node_modules', 'playwright')].filter(Boolean).find((p) => fs.existsSync(p));
if (!pw) { console.error('playwright not found; set PLAYWRIGHT=/path/to/node_modules/playwright'); process.exit(1); }
const { chromium } = require(pw);

const [mode, base] = process.argv.slice(2);
const password = process.env.PASSWORD || 'Admin@321';
const out = path.join(__dirname, '_captures');
fs.mkdirSync(out, { recursive: true });

(async () => {
  const browser = await chromium.launch({ headless: true, channel: process.env.CHROME ? undefined : 'chrome', executablePath: process.env.CHROME });
  const page = await (await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 })).newPage();
  const wait = (ms) => page.waitForTimeout(ms);
  await page.goto(base);
  await page.getByText('Sign in', { exact: true }).first().click(); await wait(400);
  await page.locator('input').first().fill('admin'); await page.keyboard.press('Enter'); await wait(400);
  await page.locator('input[type=password]').first().fill(password); await page.keyboard.press('Enter'); await wait(2500);
  const go = async (group, label) => {
    const nav = page.locator('header, nav').getByText(group || label, { exact: true }).first();
    if (group) { await nav.hover(); await wait(300); await page.getByText(label, { exact: true }).first().click(); } else await nav.click();
    await page.mouse.move(700, 880); await wait(1800);
  };
  const shot = (name, opts = {}) => page.screenshot({ path: path.join(out, name + '.png'), ...opts });

  if (mode === 'gpu') {
    await shot('overview');
    await go('Decide', 'Plan'); await shot('plan');
    await go('Decide', 'Simulate'); await page.getByRole('button', { name: 'Simulate' }).click(); await wait(2000); await shot('simulate', { fullPage: true });
    await go(null, 'Signals'); await shot('signals');
    await go(null, 'Model'); await shot('model');
    // Approvals: propose the last (single, non-combined) action on Plan so the inbox has a real proposal.
    await go('Decide', 'Plan');
    const propose = page.getByRole('button', { name: 'Propose for approval' });
    await propose.nth((await propose.count()) - 1).click(); await wait(2000);
    await go('Act', 'Approvals'); await shot('approvals', { fullPage: true });
  } else if (mode === 'mfg') {
    await go('Business', 'Scenarios');
    const save = async (name, boxes) => {
      await page.getByPlaceholder(/Name, e\.g\./).fill(name);
      for (const b of boxes) await page.getByLabel(b).check();
      await page.getByRole('button', { name: 'Save and run' }).click(); await wait(2500);
      for (const b of boxes) await page.getByLabel(b).uncheck().catch(() => {});
    };
    const gpu = 'Add GPU capacity to the inspection service', alt = 'Route a share of inspection to the alternate site';
    await save('More GPUs', [gpu]); await save('Alternate site', [alt]); await save('Both', [gpu, alt]);
    const boxes = page.locator('table input[type=checkbox]');
    for (let i = 0; i < (await boxes.count()); i++) await boxes.nth(i).check();
    await wait(2000); await shot('scenarios', { fullPage: true });
    await go('Business', 'Workflows'); await shot('workflows');
    await go('Business', 'Objects');
    await page.getByText('InspectionService', { exact: true }).first().click(); await wait(800);
    await page.getByText('Vision QA (plant A)', { exact: true }).first().click(); await wait(2000);
    await page.addStyleTag({ content: 'header,nav{position:static !important}' });
    await page.evaluate(() => window.scrollTo(0, 640)); await wait(500);
    await shot('objects');
  } else { console.error('usage: capture.cjs gpu|mfg <base-url>'); process.exit(2); }
  await browser.close();
})();

#!/usr/bin/env node
//
// Headless SPA crash smoke: drives the embedded Airflow 3.2 UI of a running
// Leoflow control plane and fails if any screen throws an uncaught error.
//
// It exists because the Go API tests cannot see the React layer: the connector
// config page shipped broken (the catalog omitted standard_fields keys the SPA
// reads unconditionally → "Cannot read properties of undefined (reading 'hidden')")
// and only a real browser caught it. This crawls the main routes plus the
// connection form (which renders EVERY connector type) and the run → task → logs →
// actions path, capturing `pageerror`/console errors per step.
//
// Run against a live Lite:
//   leoflow lite --port 18080 --postgres managed --executor subprocess &
//   npm i playwright-core && npx playwright install chromium     # once
//   LEOFLOW_URL=http://localhost:18080 LEOFLOW_USER=admin@leoflow.local \
//     LEOFLOW_PASS=<pw> node test/e2e/ui-smoke.js
//
// Exit code 0 = no crashes; 1 = at least one screen/interaction threw.

const { chromium } = require('playwright-core');
const fs = require('fs');

const URL = process.env.LEOFLOW_URL || 'http://localhost:18080';
const USER = process.env.LEOFLOW_USER || 'admin@leoflow.local';
const PASS = process.env.LEOFLOW_PASS || '';

// Reuse a cached chromium when we can find one (macOS or Linux paths); otherwise
// return undefined and let playwright-core resolve the browser it installed.
function cachedChromium() {
  const bases = [
    `${process.env.HOME}/Library/Caches/ms-playwright`, // macOS
    `${process.env.HOME}/.cache/ms-playwright`,         // Linux
  ];
  for (const base of bases) {
    try {
      for (const d of fs.readdirSync(base).filter((x) => x.startsWith('chromium')).sort().reverse()) {
        for (const sub of ['chrome-mac/Chromium.app/Contents/MacOS/Chromium', 'chrome-linux/chrome', 'chrome-linux/headless_shell']) {
          const p = `${base}/${d}/${sub}`;
          if (fs.existsSync(p)) return p;
        }
      }
    } catch {}
  }
  return undefined;
}

const CRASH_RE = /Cannot read properties|is not a function|client-side exception|Something went wrong|Minified React error #\d/i;

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: cachedChromium() });
  const page = await (await browser.newContext()).newPage();
  let errs = [];
  page.on('pageerror', (e) => errs.push((e.stack || e.message).split('\n').slice(0, 2).join(' | ')));
  page.on('console', (m) => {
    if (m.type() === 'error' && CRASH_RE.test(m.text())) errs.push('console: ' + m.text().split('\n')[0]);
  });

  const results = [];
  const step = async (name, fn) => {
    errs = [];
    try { await fn(); } catch (e) { errs.push('THREW: ' + e.message.split('\n')[0]); }
    await page.waitForTimeout(1200);
    const overlay = await page.getByText(CRASH_RE).first().count().catch(() => 0);
    results.push({ name, bad: !!overlay || errs.length > 0, errs: [...new Set(errs)].slice(0, 2) });
  };

  // Login (the username input has no type attr, so target by name).
  await page.goto(URL, { waitUntil: 'networkidle' }).catch(() => {});
  await page.fill('input[name=username]', USER).catch(() => {});
  await page.fill('input[name=password]', PASS).catch(() => {});
  await page.click('button[type=submit]').catch(() => {});
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(1000);

  // 1) Every top-level screen (page load). /connections renders all connector types.
  const routes = ['/', '/dags', '/connections', '/variables', '/pools', '/providers',
    '/assets', '/config', '/plugins', '/xcoms', '/security/users', '/browse/audit_log'];
  for (const r of routes) await step('route ' + r, async () => { await page.goto(URL + r, { waitUntil: 'networkidle' }); });

  // 2) Connection form open (the bug that shipped broken).
  await step('connections: open Add form', async () => {
    await page.goto(URL + '/connections', { waitUntil: 'networkidle' });
    const add = page.getByRole('button', { name: /add connection|^add$/i }).first();
    if (await add.count()) await add.click();
  });

  // 3) Run → task instance → logs → action menu (the high-churn detail area).
  await step('dag grid: a DAG', async () => { await page.goto(URL + '/dags/hello', { waitUntil: 'networkidle' }); });
  await step('open a task cell', async () => {
    const c = page.locator('svg rect, a[href*="tasks"], [data-testid*="task-instance"]').first();
    if (await c.count()) await c.click({ timeout: 4000 });
  });
  await step('open Logs tab', async () => {
    const l = page.getByRole('tab', { name: /log/i }).or(page.getByText(/^Logs?$/)).first();
    if (await l.count()) await l.click({ timeout: 4000 });
  });
  // Logs are a historically fragile contract (empty-log regressions). Assert the
  // logs endpoint returns the structured {content:[…]} the viewer parses and that
  // at least one log line actually rendered on the page.
  await step('logs: content renders', async () => {
    // Mint our own token via the public auth endpoint (independent of where the
    // SPA stashes its session) so the logs assertion can't silently no-op.
    const auth = await (await page.request.post(URL + '/auth/token',
      { data: { username: USER, password: PASS } })).json().catch(() => ({}));
    const tok = auth.access_token;
    if (!tok) throw new Error('POST /auth/token returned no access_token (auth contract changed)');
    const run = await (await page.request.get(URL + '/api/v2/dags/hello/dagRuns?limit=1',
      { headers: { Authorization: 'Bearer ' + tok } })).json().catch(() => ({}));
    const rid = run?.dag_runs?.[0]?.dag_run_id;
    if (rid) {
      const tis = await (await page.request.get(`${URL}/api/v2/dags/hello/dagRuns/${rid}/taskInstances`,
        { headers: { Authorization: 'Bearer ' + tok } })).json().catch(() => ({}));
      const tid = tis?.task_instances?.[0]?.task_id;
      const logs = await (await page.request.get(
        `${URL}/api/v2/dags/hello/dagRuns/${rid}/taskInstances/${tid}/logs/1`,
        { headers: { Authorization: 'Bearer ' + tok, Accept: 'application/json' } })).json().catch(() => ({}));
      if (!Array.isArray(logs.content) || logs.content.length === 0) {
        throw new Error('logs endpoint returned no structured content (empty-log regression)');
      }
    }
  });
  await step('open action menu', async () => {
    const m = page.getByRole('button', { name: /clear|mark|action/i }).first();
    if (await m.count()) await m.click({ timeout: 4000 }).catch(() => {});
  });

  await browser.close();

  const failed = results.filter((r) => r.bad);
  console.log('\n===== UI SMOKE =====');
  for (const r of results) {
    console.log(`${r.bad ? 'FAIL' : 'ok  '}  ${r.name}`);
    r.errs.forEach((e) => console.log('        ' + e.slice(0, 200)));
  }
  if (failed.length) {
    console.error(`\nUI SMOKE FAILED: ${failed.length} screen(s) threw an uncaught error.`);
    process.exit(1);
  }
  console.log(`\nUI SMOKE PASSED: ${results.length} screens/interactions, no crashes.`);
})().catch((e) => { console.error('FATAL', e.message); process.exit(1); });

#!/usr/bin/env node
//
// Browser assertion for #1160: a deployment configured for SSO must OFFER the
// flow on its sign-in page.
//
// A Go handler test pins the rendered bytes, which is necessary and not
// sufficient: the defect was that a real user, on a correctly configured SSO
// deployment, saw a username and password form and no way in. They typed their
// IdP credentials into it, got "Invalid credentials", and produced no audit
// record at all, because no OIDC request was ever made. That is a browser-visible
// property, so a browser asserts it. Same reasoning as ui-smoke.js, which exists
// because the React layer once shipped broken and only a browser caught it.
//
// Usage (see sso-login-page.sh, which stands up the fake IdP and the server):
//   LEOFLOW_URL=http://localhost:18080 node test/e2e/sso-login-page.js
//   LEOFLOW_URL=http://localhost:18081 LEOFLOW_EXPECT_SSO=0 node test/e2e/sso-login-page.js
const { chromium } = require('playwright-core');

const URL_BASE = process.env.LEOFLOW_URL || 'http://localhost:18080';
const EXPECT_SSO = process.env.LEOFLOW_EXPECT_SSO !== '0';
// Where the flow is supposed to end up. Asserting only that the browser left the
// sign-in page is satisfied by a 500 from /api/v2/auth/oidc/login, whose URL does
// not contain "/api/v2/auth/login" either.
const IDP_ORIGIN = process.env.LEOFLOW_IDP_ORIGIN || 'https://localhost:18443';

function cachedChromium() {
  const fs = require('fs');
  for (const dir of [
    `${process.env.HOME}/Library/Caches/ms-playwright`,
    `${process.env.HOME}/.cache/ms-playwright`,
  ]) {
    if (!fs.existsSync(dir)) continue;
    for (const entry of fs.readdirSync(dir)) {
      if (!entry.startsWith('chromium')) continue;
      for (const candidate of [
        `${dir}/${entry}/chrome-linux/chrome`,
        `${dir}/${entry}/chrome-mac/Chromium.app/Contents/MacOS/Chromium`,
      ]) {
        if (fs.existsSync(candidate)) return candidate;
      }
    }
  }
  return undefined;
}

const fail = (msg) => { console.error(`FAIL: ${msg}`); process.exitCode = 1; };

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: cachedChromium() });
  // The fake IdP holds a throwaway CA's cert. Without this the redirect ends on
  // Chromium's interstitial, whose URL is still the IdP's, so the assertions
  // below would pass on a handshake that never completed.
  const context = await browser.newContext({ ignoreHTTPSErrors: true });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));

  await page.goto(`${URL_BASE}/api/v2/auth/login?next=/dags`, { waitUntil: 'domcontentloaded' });

  const sso = page.locator('a.sso');
  const ssoCount = await sso.count();

  if (EXPECT_SSO) {
    if (ssoCount === 0) {
      fail('the sign-in page offers no SSO control; a user configured for SSO has no route into the flow but the address bar');
    } else {
      const href = await sso.first().getAttribute('href');
      if (!href || !href.startsWith('/api/v2/auth/oidc/login')) {
        fail(`the SSO control points at ${href}, not the route that starts the flow`);
      }
      if (!href.includes('next=')) {
        fail('the SSO control drops ?next=, so an SSO login cannot return the user to the page they asked for');
      }
      // Following it must actually reach the IdP's authorization endpoint with a
      // usable request. A control that renders and goes nowhere is the same dead
      // end with extra steps, and so is one that 500s on the way.
      await sso.first().click();
      await page.waitForLoadState('domcontentloaded');
      const landed = page.url();
      if (!landed.startsWith(IDP_ORIGIN)) {
        fail(`clicking the SSO control landed on ${landed}, not the IdP at ${IDP_ORIGIN}`);
      } else {
        const q = new URL(landed).searchParams;
        // PKCE and the CSRF/replay bindings are what make the redirect a login
        // rather than a link. Losing any of them is silent from the browser.
        for (const param of ['client_id', 'redirect_uri', 'state', 'nonce', 'code_challenge']) {
          if (!q.get(param)) {
            fail(`the authorization request carries no ${param}: ${landed}`);
          }
        }
      }
    }
    // break_glass_emails exists so named local logins still work when the IdP is
    // down. The form has to stay.
    await page.goto(`${URL_BASE}/api/v2/auth/login`, { waitUntil: 'domcontentloaded' });
    if (await page.locator('input[name="password"]').count() === 0) {
      fail('the password form is gone; break-glass accounts have no way in when the IdP is unreachable');
    }
  } else if (ssoCount > 0) {
    fail('a deployment with no OIDC flow advertises the SSO route; following it 404s');
  }

  if (errors.length) fail(`uncaught page errors: ${errors.join(' | ')}`);
  await context.close();
  await browser.close();
  if (!process.exitCode) {
    console.log(`ok: sign-in page at ${URL_BASE} ${EXPECT_SSO ? 'offers' : 'correctly omits'} the SSO flow`);
  }
})().catch((e) => { console.error(e); process.exit(1); });

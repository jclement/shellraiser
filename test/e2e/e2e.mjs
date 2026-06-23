// shellraiser end-to-end browser tests (Playwright).
// Driven by test/e2e/run.sh, which builds the binary, starts a --no-auth and an
// auth-enabled instance, and passes their URLs + the bootstrap code via env.
import { chromium } from 'playwright';

const NOAUTH = process.env.NOAUTH_URL;
const AUTH = process.env.AUTH_URL;
const BOOTSTRAP = process.env.BOOTSTRAP;

const results = [];
function check(name, ok, extra = '') {
  results.push({ name, ok });
  console.log(`${ok ? '✓' : '✗'} ${name}${extra ? '  — ' + extra : ''}`);
}

const browser = await chromium.launch();

// ---- Scenario A: no-auth UI + live terminal -------------------------------
{
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });

  await page.goto(NOAUTH, { waitUntil: 'load' });
  await page.waitForFunction(() => /\w/.test(document.querySelector('#worktrees')?.textContent || ''), { timeout: 10000 });
  check('worktree list renders', true);

  await page.click('#add-tab');
  await page.click('#launch-menu >> text=shell');
  await page.waitForSelector('.xterm', { timeout: 8000 });
  await page.waitForTimeout(1500);
  const termText = await page.evaluate(() => {
    const rec = Object.values(window.__shellraiser.terms)[0];
    if (!rec) return '';
    const buf = rec.term.buffer.active;
    let s = '';
    for (let i = 0; i < buf.length; i++) { const ln = buf.getLine(i); if (ln) s += ln.translateToString(true); }
    return s;
  });
  check('shell streams terminal output', termText.trim().length > 0);
  check('no console/page errors', errors.length === 0, errors.slice(0, 2).join(' | '));
  await ctx.close();
}

// ---- Scenario B: passkey register → logout → login ------------------------
{
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  const client = await ctx.newCDPSession(page);
  await client.send('WebAuthn.enable');
  await client.send('WebAuthn.addVirtualAuthenticator', {
    options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true },
  });

  await page.goto(AUTH, { waitUntil: 'load' });
  check('login overlay shown when auth enabled', await page.isVisible('#login'));

  await page.fill('#reg-code', BOOTSTRAP);
  await page.fill('#reg-label', 'e2e');
  await Promise.all([page.waitForNavigation({ waitUntil: 'load' }).catch(() => {}), page.click('#passkey-register')]);
  await page.waitForSelector('#worktrees', { timeout: 8000 });
  check('register passkey unlocks the app', (await page.isVisible('#app')) && !(await page.isVisible('#login')));

  await Promise.all([page.waitForNavigation({ waitUntil: 'load' }).catch(() => {}), page.click('#logout')]);
  await page.waitForSelector('#passkey-login', { state: 'visible', timeout: 8000 });
  await Promise.all([page.waitForNavigation({ waitUntil: 'load' }).catch(() => {}), page.click('#passkey-login')]);
  await page.waitForSelector('#worktrees', { timeout: 8000 });
  check('passkey login unlocks the app', (await page.isVisible('#app')) && !(await page.isVisible('#login')));
  await ctx.close();
}

// ---- Scenario C: themes render without errors -----------------------------
for (const theme of ['light', 'dark']) {
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.addInitScript((t) => localStorage.setItem('shellraiser-theme', t), theme);
  await page.goto(NOAUTH, { waitUntil: 'load' });
  await page.waitForSelector('#worktrees', { timeout: 8000 });
  check(`${theme} theme renders cleanly`, errors.length === 0, errors.slice(0, 1).join(''));
  await ctx.close();
}

await browser.close();
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
process.exit(failed.length ? 1 : 0);

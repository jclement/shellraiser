// v2 coordinator e2e: drive the unified UI through `sb` headlessly and assert the
// project-aware shell loads, worktrees flow through the /w/<id>/ proxy, and the
// coordinator root redirects to a project. Invoked by coordinator.sh.
import { chromium } from 'playwright';

const BASE = process.env.COORD_URL; // e.g. http://127.0.0.1:7790
const PROJECT = process.env.PROJECT_ID; // e.g. slopbox
let failures = 0;
const ok = (cond, msg) => { console.log(`${cond ? 'ok  ' : 'FAIL'}  ${msg}`); if (!cond) failures++; };

const browser = await chromium.launch();
const page = await browser.newPage();
const errs = [];
page.on('console', (m) => { if (m.type() === 'error') errs.push(m.text()); });

// 1. project shell loads and BASE-prefixed worker data flows through the proxy
await page.goto(`${BASE}/w/${PROJECT}/`, { waitUntil: 'domcontentloaded' });
await page.waitForFunction(
  () => window.__slopbox && window.__slopbox.worktrees && window.__slopbox.worktrees.length >= 1,
  { timeout: 20000 },
);
const s = await page.evaluate(() => ({
  projectId: window.__slopbox.projectId,
  projects: window.__slopbox.projects.length,
  worktrees: window.__slopbox.worktrees.length,
}));
ok(s.projectId === PROJECT, `active project = ${PROJECT} (got ${s.projectId})`);
ok(s.worktrees >= 1, `worktrees loaded through /w/<id>/ proxy (${s.worktrees})`);
ok(s.projects >= 1, `project rail populated (${s.projects})`);
ok(errs.length === 0, `no console errors${errs.length ? ': ' + errs.join('; ') : ''}`);

// 2. coordinator root redirects into a project
const p2 = await browser.newPage();
await p2.goto(`${BASE}/`, { waitUntil: 'domcontentloaded' });
await p2.waitForFunction(() => location.pathname.startsWith('/w/'), { timeout: 10000 }).catch(() => {});
ok(new URL(p2.url()).pathname.startsWith('/w/'), `root / redirects to a project (${new URL(p2.url()).pathname})`);

await browser.close();
console.log(failures ? `\n${failures} check(s) failed` : '\nall coordinator checks passed');
process.exit(failures ? 1 : 0);

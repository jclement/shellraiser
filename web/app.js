// shellraiser front-end: worktree nav, tabbed xterm sessions, live status + ding.
'use strict';

// kind → icon name
const KIND = { claude: 'claude', codex: 'codex', shell: 'terminal', editor: 'pencil', command: 'play' };

// Inline SVG icon set (lucide-style, 24x24, stroke=currentColor).
const ICONS = {
  box: '<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>',
  key: '<path d="m15.5 7.5 2.3 2.3a1 1 0 0 0 1.4 0l2.1-2.1a1 1 0 0 0 0-1.4L19 4"/><path d="m21 2-9.6 9.6"/><circle cx="7.5" cy="15.5" r="5.5"/>',
  terminal: '<path d="m4 17 6-6-6-6"/><path d="M12 19h8"/>',
  claude: '<path d="M9.94 14.06A2 2 0 0 0 8.5 12.6L2.4 11A.5.5 0 0 1 2.4 10l6.1-1.6A2 2 0 0 0 9.94 7L11.5.9a.5.5 0 0 1 1 0L14.06 7a2 2 0 0 0 1.44 1.44L21.6 10a.5.5 0 0 1 0 1l-6.1 1.56A2 2 0 0 0 14.06 14L12.5 20.1a.5.5 0 0 1-1 0z"/>',
  codex: '<path d="M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5a2 2 0 0 0 2 2h1"/><path d="M16 3h1a2 2 0 0 1 2 2v5a2 2 0 0 0 2 2 2 2 0 0 0-2 2v5a2 2 0 0 1-2 2h-1"/>',
  pencil: '<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/>',
  database: '<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14a9 3 0 0 0 18 0V5"/><path d="M3 12a9 3 0 0 0 18 0"/>',
  menu: '<line x1="4" x2="20" y1="6" y2="6"/><line x1="4" x2="20" y1="12" y2="12"/><line x1="4" x2="20" y1="18" y2="18"/>',
  logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" x2="9" y1="12" y2="12"/>',
  plus: '<path d="M5 12h14"/><path d="M12 5v14"/>',
  branch: '<line x1="6" x2="6" y1="3" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>',
  monitor: '<rect width="20" height="14" x="2" y="3" rx="2"/><line x1="8" x2="16" y1="21" y2="21"/><line x1="12" x2="12" y1="17" y2="21"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/>',
  moon: '<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/>',
  x: '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
  play: '<polygon points="6 3 20 12 6 21 6 3"/>',
  trash: '<path d="M3 6h18"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/>',
  external: '<path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>',
  code: '<polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/>',
};
const WT_COLORS = ['', '#ef4444', '#f59e0b', '#10b981', '#3b82f6', '#8b5cf6', '#ec4899'];
function svg(name, cls = 'icon') { return `<svg class="${cls}" viewBox="0 0 24 24">${ICONS[name] || ''}</svg>`; }
function fillIcons(root = document) {
  root.querySelectorAll('[data-icon]').forEach((e) => {
    if (e.querySelector(':scope > svg')) return;
    const cls = 'icon' + (e.classList.contains('icon-sm') ? ' icon-sm' : '') + (e.classList.contains('icon-lg') ? ' icon-lg' : '');
    e.insertAdjacentHTML('afterbegin', svg(e.dataset.icon, cls));
  });
}
const cssVar = (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
const stateColor = (s) => cssVar(s === 'running' ? '--green' : s === 'exited' ? '--red' : '--faint') || '#888';

const state = {
  info: null,
  worktrees: [],
  selected: null,        // selected worktree path (launch target)
  sessions: [],          // [{id,title,kind,cwd,state,exitCode,pid}]
  commands: [],          // custom launchers from .shellraiser.toml
  active: null,          // active tab id
  ports: [],             // [{port,process,worktree,sessionId}]
  terms: {},             // id -> { term, fit, ws, host }
  audio: null,
  ctrlSticky: false,     // mobile key-bar Ctrl modifier armed for next key
};

window.__shellraiser = state; // exposed for automated tests
const $ = (sel) => document.querySelector(sel);
const el = (tag, cls, html) => { const e = document.createElement(tag); if (cls) e.className = cls; if (html != null) e.innerHTML = html; return e; };

// The coordinator serves one UI for many projects. When the shell is mounted
// under /w/<id>/, every WORKER call (api/ws/ports/db/edit/p) is prefixed with
// that base; AUTH and the project list are coordinator routes (unprefixed).
const BASE = (location.pathname.match(/^\/w\/[^/]+/) || [''])[0];
const PROJECT_ID = BASE ? BASE.split('/')[2] : null;
state.projectId = PROJECT_ID;
state.projects = [];
state.portMaps = {}; // containerPort → host loopback port (active SSH -L tunnels)
state.unseen = {};   // worktree path → true when a session finished but wasn't viewed
state.wtFilter = ''; // quick-filter text for the worktree list
state.dragPath = null;

async function request(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error || msg; } catch (_) {}
    throw new Error(msg);
  }
  return res.status === 204 ? null : res.json();
}

// api → the active worker (prefixed under /w/<id>/). capi → the coordinator.
const api = (method, path, body) => request(method, BASE + path, body);
const capi = (method, path, body) => request(method, path, body);

function toast(msg, kind = 'error') {
  const t = el('div', 'pointer-events-auto rounded-md border px-3 py-2 text-sm text-white shadow-lg');
  t.style.background = kind === 'error' ? 'var(--red)' : 'var(--green)';
  t.style.borderColor = 'transparent';
  t.textContent = msg;
  $('#toast').appendChild(t);
  setTimeout(() => t.remove(), 4000);
}

// ---- modal dialogs (no native confirm/prompt) ----------------------------

function modal({ title, bodyHTML, fields, actions }) {
  return new Promise((resolve) => {
    const root = $('#modal-root');
    root.innerHTML = '';
    const card = el('div', 'w-[24rem] max-w-full overflow-hidden rounded-lg border border-app bg-panel shadow-2xl');
    card.appendChild(el('div', 'border-b border-app px-4 py-3 text-sm font-semibold text-app', title));
    const body = el('div', 'px-4 py-4 text-sm text-muted');
    if (bodyHTML) body.innerHTML = bodyHTML;
    const inputs = {};
    for (const f of fields || []) {
      if (f.type === 'checkbox') {
        const wrap = el('label', 'mt-3 flex cursor-pointer items-center gap-2 text-sm text-app');
        const cb = el('input'); cb.type = 'checkbox'; cb.checked = !!f.value;
        wrap.appendChild(cb); wrap.appendChild(el('span', '', f.label));
        inputs[f.name] = cb; body.appendChild(wrap); continue;
      }
      body.appendChild(el('label', 'mb-1 mt-3 block text-[11px] text-muted', f.label));
      const inp = el('input', 'input w-full px-2.5 py-2 text-sm text-app' + (f.mono ? ' tracking-wider' : ''));
      if (f.type === 'password') inp.type = 'password';
      inp.placeholder = f.placeholder || ''; inp.value = f.value || '';
      if (f.datalist && f.datalist.length) {
        const dl = el('datalist'); dl.id = 'dl-' + f.name;
        for (const o of f.datalist) { const opt = el('option'); opt.value = o; dl.appendChild(opt); }
        inp.setAttribute('list', dl.id); body.appendChild(dl);
      }
      inputs[f.name] = inp; body.appendChild(inp);
    }
    card.appendChild(body);
    const foot = el('div', 'flex justify-end gap-2 border-t border-app px-4 py-3');
    const done = (val) => { root.classList.add('hidden'); root.classList.remove('flex'); document.removeEventListener('keydown', onKey); resolve(val); };
    const values = () => Object.fromEntries(Object.entries(inputs).map(([k, v]) => [k, v.type === 'checkbox' ? v.checked : v.value.trim()]));
    for (const a of actions || []) {
      const cls = a.primary ? 'btn-primary' : a.danger ? 'btn-danger' : '';
      const b = el('button', `btn px-3 py-1.5 text-sm ${cls}`, a.label);
      b.dataset.role = a.primary || a.danger ? 'go' : 'cancel';
      b.onclick = () => done(a.value !== undefined ? a.value : values());
      foot.appendChild(b);
    }
    card.appendChild(foot);
    root.appendChild(card);
    root.classList.remove('hidden'); root.classList.add('flex');
    root.onclick = (e) => { if (e.target === root) done(null); };
    const onKey = (e) => {
      if (e.key === 'Escape') done(null);
      if (e.key === 'Enter') { const go = foot.querySelector('[data-role="go"]'); if (go) go.click(); }
    };
    document.addEventListener('keydown', onKey);
    const first = Object.values(inputs)[0]; if (first) setTimeout(() => first.focus(), 0);
  });
}

async function confirmModal(title, bodyHTML, opts = {}) {
  const v = await modal({
    title, bodyHTML, actions: [
      { label: 'Cancel', value: false },
      { label: opts.confirmLabel || 'Confirm', primary: !opts.danger, danger: opts.danger, value: true },
    ],
  });
  return v === true;
}

async function promptModal(title, field, confirmLabel = 'OK') {
  const v = await modal({ title, fields: [field], actions: [{ label: 'Cancel', value: null }, { label: confirmLabel, primary: true }] });
  return v ? v[field.name] : null;
}

// ---- projects (cross-project rail) ----------------------------------------

async function loadProjects() {
  try { state.projects = await capi('GET', '/api/workers'); } catch (_) { state.projects = []; }
  renderProjects();
}

function renderProjects() {
  const nav = $('#projects');
  const section = $('#projects-section');
  if (!nav) return;
  // Only show the rail when the coordinator fronts more than one project (or we
  // are at the coordinator root with a project list to choose from).
  const show = state.projects.length > 0 && (state.projects.length > 1 || !PROJECT_ID);
  section.classList.toggle('hidden', !show);
  nav.innerHTML = '';
  for (const p of state.projects) {
    const active = p.id === state.projectId;
    const row = el('div', `group flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm ${active ? 'row-sel' : 'hover-row'}`);
    const ic = el('span', active ? 'text-accent' : 'text-faint'); ic.innerHTML = svg('box', 'icon icon-sm'); row.appendChild(ic);
    const name = el('span', 'truncate ' + (active ? 'text-app font-medium' : 'text-muted'), p.name); name.title = p.project; row.appendChild(name);
    const st = el('span', 'ml-auto flex shrink-0 items-center gap-1');
    if (p.state !== 'running') st.appendChild(el('span', 'text-[10px] text-faint', p.state));
    // container controls
    const kebab = el('button', 'iconbtn px-1 opacity-0 transition group-hover:opacity-100'); kebab.title = 'Manage'; kebab.innerHTML = svg('trash', 'icon icon-sm');
    kebab.onclick = (ev) => { ev.stopPropagation(); manageProject(p); };
    st.appendChild(kebab);
    row.appendChild(st);
    row.onclick = () => { if (!active) location.href = '/w/' + p.id + '/'; };
    nav.appendChild(row);
  }
}

async function manageProject(p) {
  const v = await modal({
    title: `Manage ${p.name}`,
    bodyHTML: `<div class="text-muted">Stop pauses the container (data kept). Nuke removes the container, its volume, and network — your project source on disk is never touched.</div>`,
    actions: [
      { label: 'Cancel', value: null },
      { label: p.state === 'running' ? 'Stop' : 'Start', value: p.state === 'running' ? 'stop' : 'start' },
      { label: 'Nuke', danger: true, value: 'nuke' },
    ],
  });
  if (!v) return;
  if (v === 'nuke' && !(await confirmModal('Nuke project', `Remove <b class="text-app">${p.name}</b>'s container + volume + network?`, { danger: true, confirmLabel: 'Nuke' }))) return;
  try {
    await capi('POST', '/api/workers/' + p.id + '/' + v, {});
    if (v === 'nuke' && p.id === state.projectId) { location.href = '/'; return; }
    await loadProjects();
  } catch (e) { toast(e.message); }
}

// ---- data loading ---------------------------------------------------------

async function loadInfo() {
  state.info = await api('GET', '/api/info');
  $('#repo-name').textContent = state.info.repo;
  $('#repo-name').title = `repository: ${state.info.repoDir}`;
  if (!state.selected) state.selected = state.info.repoDir;
  const db = $('#db-btn');
  db.classList.toggle('hidden', !state.info.postgres);
  db.onclick = () => window.open(BASE + '/db/', '_blank');
  const ssh = $('#ssh-copy');
  ssh.classList.toggle('hidden', !state.info.ssh);
  ssh.onclick = copySSHCommand;
}

// Mint a short-lived SSH key + a ready-to-paste command that forwards every
// running port, and offer to copy it.
async function copySSHCommand() {
  let res;
  try { res = await api('POST', '/api/ssh/command', {}); } catch (e) { toast(e.message); return; }
  const safe = res.command.replace(/&/g, '&amp;').replace(/</g, '&lt;');
  const body = `<div class="mb-2 text-muted">Run this on your machine — forwards ${res.ports.length} port(s) via <span class="text-app">${res.host}:${res.port}</span>, valid ~${Math.round(res.ttlSeconds / 60)} min.</div>`
    + `<pre class="max-h-56 overflow-auto rounded border border-app bg-panel2 p-2 text-[11px] text-app" style="white-space:pre-wrap">${safe}</pre>`;
  const v = await modal({ title: 'SSH command (ephemeral)', bodyHTML: body, actions: [{ label: 'Close', value: null }, { label: 'Copy', primary: true, value: 'copy' }] });
  if (v === 'copy') {
    try { await navigator.clipboard.writeText(res.command); toast('SSH command copied', 'ok'); }
    catch (_) { toast('Copy failed — select the text in the box'); }
  }
}

const BUILTIN_LAUNCH = [
  { kind: 'claude', icon: 'claude', label: 'claude' },
  { kind: 'codex', icon: 'codex', label: 'codex' },
  { kind: 'shell', icon: 'terminal', label: 'shell' },
  { kind: 'editor', icon: 'pencil', label: 'editor' },          // helix/$EDITOR
  { kind: 'editor', icon: 'pencil', label: 'fresh', args: ['fresh'], title: 'fresh' },
];

async function loadCommands() {
  try { state.commands = await api('GET', '/api/commands'); } catch (_) { state.commands = []; }
  renderLaunchMenu();
}

function renderLaunchMenu() {
  const menu = $('#launch-menu');
  menu.innerHTML = '';
  const item = (icon, label, onClick) => {
    const b = el('button', 'flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-app hover-row');
    b.innerHTML = svg(icon, 'icon icon-sm') + `<span>${label}</span>`;
    b.onclick = () => { menu.classList.add('hidden'); onClick(); };
    return b;
  };
  for (const b of BUILTIN_LAUNCH) menu.appendChild(item(b.icon, b.label, () => launch(b.kind, b.args, b.title)));
  if (state.commands.length) {
    menu.appendChild(el('div', 'my-1 border-t border-app'));
    for (const c of state.commands) menu.appendChild(item('play', c.name, () => launchCommand(c.name)));
  }
}

async function loadWorktrees() {
  state.worktrees = await api('GET', '/api/worktrees');
  // ensure selection still valid
  if (!state.worktrees.some((w) => w.path === state.selected)) {
    state.selected = state.info ? state.info.repoDir : (state.worktrees[0] && state.worktrees[0].path);
  }
  renderWorktrees();
  renderContext();
}

async function loadSessions() {
  state.sessions = await api('GET', '/api/sessions');
  reconcileTerms();
  if (state.selected) syncTabs();
  renderWorktrees();
  renderTabs();
}

async function loadPorts() {
  try { state.ports = await api('GET', '/api/ports'); } catch (_) { state.ports = []; }
  await loadPortMaps();
  renderPorts();
  renderWorktrees();
}

// ---- rendering ------------------------------------------------------------

function stateOf(id) {
  const s = state.sessions.find((x) => x.id === id);
  return s ? s.state : 'idle';
}

function dot(st) {
  // A running session gets an animated "ping" halo + solid core (busy indicator).
  const wrap = el('span', 'relative inline-flex h-2.5 w-2.5 shrink-0 items-center justify-center');
  if (st === 'running') {
    const ping = el('span', 'absolute inline-flex h-full w-full animate-ping rounded-full opacity-75');
    ping.style.background = stateColor('running');
    wrap.appendChild(ping);
  }
  const core = el('span', 'relative inline-flex h-2 w-2 rounded-full');
  core.style.background = stateColor(st);
  wrap.appendChild(core);
  return wrap;
}

function gitBadge(w) {
  const box = el('span', 'ml-auto flex shrink-0 items-center gap-1.5 pl-2 text-[11px]');
  if (w.added || w.deleted) {
    const stat = el('span', 'flex items-center gap-1 rounded bg-panel2 px-1.5 py-px');
    const a = el('span', '', `+${w.added}`); a.style.color = cssVar('--green');
    const d = el('span', '', `−${w.deleted}`); d.style.color = cssVar('--red');
    stat.appendChild(a); stat.appendChild(d);
    box.appendChild(stat);
  }
  if (w.isMain) box.appendChild(el('span', 'rounded bg-panel2 px-1.5 py-px text-muted', 'main'));
  return box;
}

function gitMeta(w) {
  // Second-line indicators: dirty · commits ahead of base · ↑/↓ vs origin.
  const parts = [];
  if (w.dirty) parts.push('<span style="color:var(--yellow)" title="uncommitted changes">● dirty</span>');
  if (w.ahead) parts.push(`<span class="text-muted" title="commits ahead of base">${w.ahead} commit${w.ahead === 1 ? '' : 's'}</span>`);
  if (w.hasUpstream && (w.aheadOrigin || w.behindOrigin)) {
    let s = '';
    if (w.aheadOrigin) s += `↑${w.aheadOrigin}`;
    if (w.behindOrigin) s += ` ↓${w.behindOrigin}`;
    parts.push(`<span class="text-accent" title="vs origin">${s.trim()}</span>`);
  } else if (w.hasUpstream) {
    parts.push('<span style="color:var(--green)" title="in sync with origin">✓ origin</span>');
  }
  return parts.join('<span class="text-faint"> · </span>');
}

// Aggregate per-worktree status (the rows show this, not the tab list):
//   working   — a session in it is running
//   attention — a session finished while you weren't looking at it
//   idle      — nothing running, nothing unseen
function worktreeStatus(path) {
  const kids = state.sessions.filter((s) => s.cwd === path);
  if (kids.some((s) => s.state === 'running')) return 'working';
  if (state.unseen[path]) return 'attention';
  return 'idle';
}

function statusDot(status) {
  const v = status === 'working' ? '--green' : status === 'attention' ? '--yellow' : '--faint';
  const wrap = el('span', 'relative ml-auto inline-flex h-2.5 w-2.5 shrink-0 items-center justify-center');
  wrap.title = status === 'working' ? 'working' : status === 'attention' ? 'finished — not looked at' : 'idle';
  if (status === 'working' || status === 'attention') {
    const ping = el('span', 'absolute inline-flex h-full w-full animate-ping rounded-full opacity-60');
    ping.style.background = cssVar(v); wrap.appendChild(ping);
  }
  const core = el('span', 'relative inline-flex h-2 w-2 rounded-full');
  core.style.background = cssVar(v); wrap.appendChild(core);
  return wrap;
}

// Dense one-line git summary for the worktree row's second line.
function wtMetaLine(w) {
  const parts = [];
  if (w.dirty) parts.push('<span style="color:var(--yellow)" title="uncommitted changes">●</span>');
  if (w.added || w.deleted) parts.push(`<span style="color:var(--green)">+${w.added}</span><span style="color:var(--red)">−${w.deleted}</span>`);
  if (w.ahead) parts.push(`<span class="text-muted" title="commits ahead of base">${w.ahead}↟</span>`);
  if (w.hasUpstream && (w.aheadOrigin || w.behindOrigin)) {
    let s = ''; if (w.aheadOrigin) s += `↑${w.aheadOrigin}`; if (w.behindOrigin) s += `↓${w.behindOrigin}`;
    parts.push(`<span class="text-accent" title="vs origin">${s}</span>`);
  }
  return parts.join(' ');
}

function renderWorktrees() {
  const nav = $('#worktrees');
  nav.innerHTML = '';
  const f = state.wtFilter.toLowerCase();
  const list = state.worktrees.filter((w) => !f ||
    (w.displayName || w.name || '').toLowerCase().includes(f) || (w.branch || '').toLowerCase().includes(f));
  for (const w of list) {
    const sel = w.path === state.selected;
    const row = el('div', `wt-row group relative mb-px cursor-pointer rounded px-2 py-1 transition ${sel ? 'row-sel' : 'hover-row'}`);
    row.draggable = true;
    row.dataset.path = w.path;
    if (w.color) { row.style.borderLeft = `3px solid ${w.color}`; row.style.paddingLeft = '0.45rem'; }
    // line 1: name (+ main tag) + aggregate status dot
    const top = el('div', 'flex items-center gap-1.5');
    const name = el('span', 'truncate text-[13px] leading-tight ' + (sel ? 'text-app font-medium' : 'text-app'), w.displayName || w.name);
    if (w.displayName) name.title = `${w.name} · ${w.branch || ''}`;
    top.appendChild(name);
    if (w.isMain) top.appendChild(el('span', 'shrink-0 rounded bg-panel2 px-1 text-[9px] uppercase text-faint', 'main'));
    top.appendChild(statusDot(worktreeStatus(w.path)));
    row.appendChild(top);
    // line 2: branch + dense git meta
    const sub = el('div', 'flex items-center gap-1.5 truncate text-[11px] leading-tight text-faint');
    sub.appendChild(el('span', 'truncate', w.detached ? `@${(w.head || '').slice(0, 7)}` : (w.branch || '')));
    const meta = wtMetaLine(w);
    if (meta) { const m = el('span', 'shrink-0'); m.innerHTML = meta; sub.appendChild(m); }
    row.appendChild(sub);
    row.onclick = () => selectWorktree(w.path);
    row.ondblclick = () => maybeCloseSidebar();
    wireDrag(row);
    nav.appendChild(row);
  }
}

// ---- drag-and-drop reorder ------------------------------------------------

function wireDrag(row) {
  row.addEventListener('dragstart', (e) => { state.dragPath = row.dataset.path; row.classList.add('opacity-40'); e.dataTransfer.effectAllowed = 'move'; });
  row.addEventListener('dragend', () => { row.classList.remove('opacity-40'); document.querySelectorAll('.drag-over').forEach((n) => n.classList.remove('drag-over')); });
  row.addEventListener('dragover', (e) => { e.preventDefault(); row.classList.add('drag-over'); });
  row.addEventListener('dragleave', () => row.classList.remove('drag-over'));
  row.addEventListener('drop', (e) => { e.preventDefault(); row.classList.remove('drag-over'); dropWorktree(state.dragPath, row.dataset.path); });
}

function dropWorktree(from, to) {
  if (!from || from === to) return;
  const arr = state.worktrees.slice();
  const fi = arr.findIndex((w) => w.path === from), ti = arr.findIndex((w) => w.path === to);
  if (fi < 0 || ti < 0) return;
  const [moved] = arr.splice(fi, 1);
  arr.splice(ti, 0, moved);
  state.worktrees = arr;
  renderWorktrees();
  api('POST', '/api/worktrees/reorder', { paths: arr.map((w) => w.path) }).catch(() => {});
}

// renderContext fills the top bar: selected worktree + its workspace actions
// (color / rename / open-in-editor / delete) — these used to clutter every row.
function renderContext() {
  const w = state.worktrees.find((x) => x.path === state.selected);
  const box = $('#active-context');
  box.innerHTML = '';
  if (!w) { box.appendChild(el('span', 'text-muted', 'Select a worktree')); return; }
  const ic = el('span', ''); ic.style.color = w.color || cssVar('--accent'); ic.innerHTML = svg('branch', 'icon icon-sm'); box.appendChild(ic);
  box.appendChild(el('span', 'truncate font-semibold text-app', w.displayName || w.name));
  box.appendChild(el('span', 'shrink-0 text-faint', '·'));
  box.appendChild(el('span', 'truncate text-muted', w.detached ? 'detached' : (w.branch || '')));
  const acts = el('span', 'ml-auto flex shrink-0 items-center gap-0.5 pl-2');
  const colorBtn = el('button', 'iconbtn px-1'); colorBtn.title = 'Color';
  colorBtn.innerHTML = `<span class="inline-block h-3 w-3 rounded-full" style="background:${w.color || 'transparent'};box-shadow:inset 0 0 0 1px var(--border)"></span>`;
  colorBtn.onclick = (ev) => { ev.stopPropagation(); openColorPicker(w, colorBtn); };
  acts.appendChild(colorBtn);
  const ren = el('button', 'iconbtn px-1'); ren.title = 'Rename'; ren.innerHTML = svg('pencil', 'icon icon-sm');
  ren.onclick = () => renameWorktree(w); acts.appendChild(ren);
  if (state.info && state.info.editor) {
    const edit = el('button', 'iconbtn px-1'); edit.title = 'Open editor (code-server)'; edit.innerHTML = svg('code', 'icon icon-sm');
    edit.onclick = () => window.open(BASE + '/edit/?folder=' + encodeURIComponent(w.path), '_blank'); acts.appendChild(edit);
  }
  if (!w.isMain) {
    const del = el('button', 'iconbtn px-1'); del.title = 'Delete worktree'; del.innerHTML = svg('trash', 'icon icon-sm');
    del.onclick = () => deleteWorktree(w); acts.appendChild(del);
  }
  box.appendChild(acts);
}

function renderTabs() {
  const bar = $('#tabs');
  bar.innerHTML = '';
  for (const id of tabsForSelected()) {
    const s = state.sessions.find((x) => x.id === id) || { id, title: '?', kind: 'shell', state: 'exited' };
    const act = id === state.active;
    const tab = el('div', `flex cursor-pointer items-center gap-2 border-r border-app px-3 py-2 ${act ? 'bg-app text-app' : 'bg-panel text-muted hover-row'}`);
    tab.appendChild(dot(s.state));
    const ki = el('span', ''); ki.innerHTML = svg(KIND[s.kind] || 'play', 'icon icon-sm'); tab.appendChild(ki);
    tab.appendChild(el('span', 'whitespace-nowrap text-xs', s.title));
    const x = el('button', 'iconbtn ml-1 px-1'); x.innerHTML = svg('x', 'icon icon-sm');
    x.onclick = (ev) => { ev.stopPropagation(); killSession(id); };
    tab.appendChild(x);
    tab.onclick = () => setActive(id);
    bar.appendChild(tab);
  }
}

// One row per port: a map toggle (maps the container port to a host-loopback
// port via SSH -L, default same number, local port editable) + the port label +
// an open-/p/ link.
function portRow(p) {
  const host = state.portMaps[p.port];
  const mapped = host != null;
  const row = el('div', 'flex items-center gap-1.5 rounded px-1 py-0.5 hover-row');

  // map toggle (filled green dot when mapped)
  const toggle = el('button', 'iconbtn flex h-4 w-4 shrink-0 items-center justify-center');
  toggle.innerHTML = `<span class="inline-block h-2.5 w-2.5 rounded-full" style="background:${mapped ? 'var(--green)' : 'transparent'};box-shadow:inset 0 0 0 1px var(--border)"></span>`;
  toggle.title = mapped ? `mapped → 127.0.0.1:${host} (click to unmap)` : 'Map to localhost';
  row.appendChild(toggle);

  // port number + process name
  const label = el('span', 'truncate text-app', String(p.port));
  if (p.process) label.appendChild(el('span', 'pl-1 text-faint', p.process));
  label.title = p.process || '';
  row.appendChild(label);

  // editable local port (defaults to the same number; tweak before/while mapping)
  const right = el('div', 'ml-auto flex shrink-0 items-center gap-1');
  const local = el('input', 'input w-12 px-1 py-0.5 text-right text-[11px] text-app');
  local.value = String(mapped ? host : p.port);
  local.title = 'Local port';
  local.onclick = (e) => e.stopPropagation();
  local.onchange = () => { const v = parseInt(local.value, 10) || p.port; if (mapped) mapPort(p.port, v); };
  right.appendChild(local);

  toggle.onclick = (e) => { e.stopPropagation(); mapped ? unmapPort(p.port) : mapPort(p.port, parseInt(local.value, 10) || p.port); };

  // open through the /p/ HTTP proxy
  const open = el('a', 'iconbtn px-0.5'); open.title = `Open /p/${p.port}/`;
  open.innerHTML = svg('external', 'icon icon-sm');
  open.href = `${BASE}/p/${p.port}/`; open.target = '_blank';
  right.appendChild(open);
  row.appendChild(right);
  return row;
}

async function loadPortMaps() {
  if (!state.projectId) return;
  try {
    const list = await capi('GET', `/api/workers/${state.projectId}/ports`);
    state.portMaps = Object.fromEntries((list || []).map((m) => [m.container, m.host]));
  } catch (_) { state.portMaps = {}; }
}

async function mapPort(port, local) {
  try {
    const r = await capi('POST', `/api/workers/${state.projectId}/ports/${port}/map`, { local: local || 0 });
    state.portMaps[port] = r.host;
    toast(`Mapped ${port} → 127.0.0.1:${r.host}`, 'ok');
    renderPorts();
  } catch (e) { toast(e.message); }
}

async function unmapPort(port) {
  try {
    await capi('POST', `/api/workers/${state.projectId}/ports/${port}/unmap`, {});
    delete state.portMaps[port];
    renderPorts();
  } catch (e) { toast(e.message); }
}

function renderPorts() {
  // Scoped to the selected worktree, one row per port, sorted by number.
  const box = $('#ports');
  box.innerHTML = '';
  const mine = state.ports
    .filter((p) => p.worktree && p.worktree === state.selected)
    .sort((a, b) => a.port - b.port);
  $('#ports-count').textContent = mine.length ? mine.length : '';
  if (!mine.length) { box.appendChild(el('div', 'text-faint', 'none')); return; }
  for (const p of mine) box.appendChild(portRow(p));
}

// ---- sessions & terminals -------------------------------------------------

async function launch(kind, args, title) {
  if (!state.selected) { toast('Pick a worktree first'); return; }
  unlockAudio();
  try {
    const s = await api('POST', '/api/sessions', { kind, cwd: state.selected, args, title });
    await loadSessions();
    openTab(s);
  } catch (e) { toast(e.message); }
}

async function launchCommand(name) {
  if (!state.selected) { toast('Pick a worktree first'); return; }
  unlockAudio();
  try {
    const s = await api('POST', '/api/sessions', { command: name, cwd: state.selected });
    await loadSessions();
    openTab(s);
  } catch (e) { toast(e.message); }
}

function openTab(s) {
  if (state.selected !== s.cwd) { state.selected = s.cwd; renderWorktrees(); renderContext(); renderPorts(); }
  syncTabs();
  setActive(s.id);
  maybeCloseSidebar();
}

function setActive(id) {
  state.active = id;
  for (const [tid, t] of Object.entries(state.terms)) {
    t.host.classList.toggle('hidden', tid !== id);
  }
  $('#empty-state').style.display = id ? 'none' : 'flex';
  const t = state.terms[id];
  if (t) { requestAnimationFrame(() => { fit(t); t.term.focus(); }); }
  updateMobileChrome();
  renderTabs();
}

// Tabs are ALL sessions in the selected worktree — selecting a worktree shows
// its live sessions without re-clicking them.
function selectedSessions() { return state.sessions.filter((s) => s.cwd === state.selected); }
function tabsForSelected() { return selectedSessions().map((s) => s.id); }
function syncTabs() { for (const s of selectedSessions()) ensureTerm(s); }

function selectWorktree(path) {
  state.selected = path;
  delete state.unseen[path]; // looked at → clear the attention indicator
  renderWorktrees(); renderContext(); renderPorts();
  syncTabs();
  const ids = tabsForSelected();
  setActive(ids.includes(state.active) ? state.active : (ids[ids.length - 1] || null));
}

function ensureTerm(s) {
  if (state.terms[s.id]) return;
  const host = el('div', 'term-host hidden');
  $('#term-area').appendChild(host);

  const term = new Terminal({
    cursorBlink: true,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
    fontSize: isMobile() ? 14 : 13,
    theme: {
      background: cssVar('--term-bg') || '#0a0a0b',
      foreground: cssVar('--term-fg') || '#e4e4e7',
      cursor: cssVar('--accent') || '#a855f7',
      selectionBackground: '#33415580',
    },
    scrollback: 10000,
  });
  const fitAddon = new FitAddon.FitAddon();
  term.loadAddon(fitAddon);
  try { term.loadAddon(new WebLinksAddon.WebLinksAddon()); } catch (_) {}
  term.open(host);

  const overlay = el('div', 'term-reconnecting hidden', 'reconnecting…');
  host.appendChild(overlay);

  const rec = { term, fit: fitAddon, ws: null, host, overlay, cwd: s.cwd, closed: false, retries: 0, retryTimer: null };
  state.terms[s.id] = rec;
  fit(rec);
  connectWS(s.id);

  term.onData((d) => sendData(rec, applyCtrl(d)));
  term.onResize(({ cols, rows }) => { if (rec.ws && rec.ws.readyState === 1) rec.ws.send(JSON.stringify({ type: 'resize', cols, rows })); });
  // Tap the terminal to (re)focus the hidden textarea → pops the soft keyboard.
  host.addEventListener('pointerup', () => { if (state.terms[state.active] === rec) focusActiveTerm(); });
}

// ---- mobile keyboard ------------------------------------------------------

function sendData(rec, data) {
  if (rec && rec.ws && rec.ws.readyState === 1) rec.ws.send(JSON.stringify({ type: 'data', data }));
}
function applyCtrl(d) {
  if (state.ctrlSticky && d.length === 1) {
    const code = d.toUpperCase().charCodeAt(0);
    if (code >= 64 && code <= 95) d = String.fromCharCode(code & 0x1f); // Ctrl-<char>
    setCtrlSticky(false);
  }
  return d;
}
function setCtrlSticky(on) {
  state.ctrlSticky = on;
  const btn = document.querySelector('.kbkey[data-k="ctrl"]');
  if (btn) btn.classList.toggle('sticky-on', on);
}
// Focus the active terminal's hidden textarea inside a user gesture so mobile
// browsers raise the keyboard (they won't focus the xterm canvas on their own).
function focusActiveTerm() {
  const rec = state.terms[state.active];
  if (!rec) return;
  const ta = rec.host.querySelector('.xterm-helper-textarea');
  if (ta) { try { ta.focus({ preventScroll: true }); } catch (_) { ta.focus(); } }
  rec.term.focus();
}
const KEYBAR = [
  { l: 'esc', d: '\x1b' }, { l: 'tab', d: '\t' }, { l: 'ctrl', k: 'ctrl' },
  { l: '←', d: '\x1b[D' }, { l: '↓', d: '\x1b[B' }, { l: '↑', d: '\x1b[A' }, { l: '→', d: '\x1b[C' },
  { l: '|', d: '|' }, { l: '~', d: '~' }, { l: '/', d: '/' }, { l: '-', d: '-' }, { l: '⇞', d: '\x1b[5~' }, { l: '⇟', d: '\x1b[6~' },
];
function buildKeyBar() {
  const bar = $('#keybar'); bar.innerHTML = '';
  for (const key of KEYBAR) {
    const b = el('button', 'kbkey', key.l);
    if (key.k) b.dataset.k = key.k;
    // pointerdown + preventDefault so tapping a key never blurs the textarea.
    b.addEventListener('pointerdown', (e) => {
      e.preventDefault();
      if (key.k === 'ctrl') { setCtrlSticky(!state.ctrlSticky); return; }
      const rec = state.terms[state.active];
      if (rec) sendData(rec, applyCtrl(key.d));
      focusActiveTerm();
    });
    bar.appendChild(b);
  }
}
function updateMobileChrome() {
  const show = !!(isMobile() && state.active && state.terms[state.active]);
  $('#keybar').classList.toggle('on', show);
  $('#kb-fab').classList.toggle('on', show);
}

// connectWS opens the terminal socket and reconnects with exponential backoff
// after any drop (sleep/wake, mobile handoff, proxy idle-timeout, server blip).
// The server replays scrollback on every attach, so we reset() before a
// reconnect's replay to avoid duplicated history.
function connectWS(id) {
  const rec = state.terms[id];
  if (!rec) return;
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}${BASE}/ws/term/${id}`);
  ws.binaryType = 'arraybuffer';
  rec.ws = ws;
  ws.onmessage = (ev) => {
    if (typeof ev.data === 'string') rec.term.write(ev.data);
    else rec.term.write(new Uint8Array(ev.data));
  };
  ws.onopen = () => {
    rec.overlay.classList.add('hidden');
    if (rec.retries > 0) rec.term.reset(); // clear before the replayed snapshot
    rec.retries = 0;
    const { cols, rows } = rec.term;
    ws.send(JSON.stringify({ type: 'resize', cols, rows }));
  };
  ws.onerror = () => {};
  ws.onclose = () => {
    rec.ws = null;
    if (rec.closed) return;
    const s = state.sessions.find((x) => x.id === id);
    if (!s || s.state === 'exited') return; // session gone/finished — don't retry
    rec.overlay.classList.remove('hidden');
    const delay = Math.min(15000, 500 * Math.pow(1.6, rec.retries++)) + Math.random() * 300;
    rec.retryTimer = setTimeout(() => connectWS(id), delay);
  };
}

function disposeTerm(id) {
  const rec = state.terms[id];
  if (!rec) return;
  rec.closed = true;
  clearTimeout(rec.retryTimer);
  if (rec.ws) rec.ws.close();
  rec.term.dispose();
  rec.host.remove();
  delete state.terms[id];
}

// reconcileTerms disposes terminals whose session vanished server-side (worktree
// deleted via CLI, external kill, reap) — otherwise they leak ws/xterm/DOM.
function reconcileTerms() {
  for (const id of Object.keys(state.terms)) {
    if (!state.sessions.find((s) => s.id === id)) {
      disposeTerm(id);
      if (state.active === id) state.active = null;
    }
  }
}

function fit(rec) {
  try { rec.fit.fit(); } catch (_) {}
}

async function killSession(id) {
  try { await api('DELETE', `/api/sessions/${id}`); } catch (e) { /* may already be gone */ }
  disposeTerm(id);
  await loadSessions();
  syncTabs();
  if (state.active === id) {
    const ids = tabsForSelected();
    setActive(ids[ids.length - 1] || null);
  } else { renderTabs(); }
}

// ---- live status (SSE) + ding --------------------------------------------

function connectEvents() {
  const es = new EventSource(BASE + '/api/events');
  es.onmessage = (ev) => {
    let m; try { m = JSON.parse(ev.data); } catch (_) { return; }
    const s = state.sessions.find((x) => x.id === m.id);
    if (s) { s.state = m.state; if (m.state === 'exited') s.exitCode = m.exitCode; }
    // A worktree you're not currently looking at that just finished gets the
    // "attention" indicator until you select it.
    if (m.ding && s && s.cwd !== state.selected) state.unseen[s.cwd] = true;
    renderWorktrees();
    renderTabs();
    if (m.ding) { ding(); flashTitle(`✅ ${m.title} done`); }
  };
  es.onerror = () => { /* EventSource auto-reconnects */ };
}

function ding() {
  try {
    const ctx = state.audio || (state.audio = new (window.AudioContext || window.webkitAudioContext)());
    const now = ctx.currentTime;
    [880, 1320].forEach((f, i) => {
      const o = ctx.createOscillator(), g = ctx.createGain();
      o.type = 'sine'; o.frequency.value = f;
      o.connect(g); g.connect(ctx.destination);
      const t = now + i * 0.12;
      g.gain.setValueAtTime(0, t);
      g.gain.linearRampToValueAtTime(0.18, t + 0.02);
      g.gain.exponentialRampToValueAtTime(0.001, t + 0.25);
      o.start(t); o.stop(t + 0.26);
    });
  } catch (_) {}
}

function unlockAudio() {
  try {
    if (!state.audio) state.audio = new (window.AudioContext || window.webkitAudioContext)();
    if (state.audio.state === 'suspended') state.audio.resume();
  } catch (_) {}
}

let titleTimer = null;
function flashTitle(msg) {
  document.title = msg;
  clearTimeout(titleTimer);
  titleTimer = setTimeout(() => { document.title = 'shellraiser'; }, 5000);
}

// ---- new worktree dialog (minimal) ---------------------------------------

async function newWorktree() {
  let branches = [];
  try { branches = (await api('GET', '/api/branches')) || []; } catch (_) {}
  const res = await modal({
    title: 'New worktree',
    bodyHTML: '<div class="text-muted">Type a new branch name to create it (off HEAD), or pick an existing branch to check it out.</div>',
    fields: [{ name: 'branch', label: 'Branch', placeholder: 'feature/my-thing', datalist: branches }],
    actions: [{ label: 'Cancel', value: null }, { label: 'Create', primary: true }],
  });
  if (!res || !res.branch) return;
  const branch = res.branch;
  const newBranch = !branches.includes(branch);
  try {
    await api('POST', '/api/worktrees', { name: branch, branch, newBranch });
    await loadWorktrees();
    toast(`${newBranch ? 'Created' : 'Checked out'} worktree ${branch}`, 'ok');
  } catch (e) { toast(e.message); }
}

async function renameWorktree(w) {
  const name = await promptModal('Rename worktree', { name: 'name', label: 'Display name (independent of branch)', placeholder: w.branch || w.name, value: w.displayName || '' }, 'Rename');
  if (name === null) return;
  try { await api('POST', '/api/worktrees/rename', { path: w.path, name }); w.displayName = name; renderWorktrees(); renderContext(); }
  catch (e) { toast(e.message); }
}

function openColorPicker(w, anchor) {
  document.querySelectorAll('.color-pop').forEach((e) => e.remove());
  const pop = el('div', 'color-pop fixed z-50 flex gap-1.5 rounded-md border border-app bg-panel p-2 shadow-lg');
  const r = anchor.getBoundingClientRect();
  pop.style.top = `${r.bottom + 4}px`;
  pop.style.visibility = 'hidden'; // measure before placing so it never runs off-screen
  for (const c of WT_COLORS) {
    const sw = el('button', 'flex h-5 w-5 items-center justify-center rounded-full text-xs text-faint');
    sw.style.background = c || 'transparent';
    sw.style.boxShadow = 'inset 0 0 0 1px var(--border)';
    if (!c) sw.textContent = '∅';
    sw.onclick = async (ev) => {
      ev.stopPropagation();
      try { await api('POST', '/api/worktrees/color', { path: w.path, color: c }); w.color = c; renderWorktrees(); }
      catch (e) { toast(e.message); }
      pop.remove();
    };
    pop.appendChild(sw);
  }
  document.body.appendChild(pop);
  // Right-align to the anchor and clamp into the viewport (the button now lives
  // at the right edge of the top bar, so a left-anchored popup ran off-screen).
  const pw = pop.getBoundingClientRect().width;
  let left = Math.min(r.right - pw, window.innerWidth - pw - 8);
  pop.style.left = `${Math.max(8, left)}px`;
  pop.style.visibility = 'visible';
  setTimeout(() => document.addEventListener('click', function h() { pop.remove(); document.removeEventListener('click', h); }), 0);
}

async function deleteWorktree(w) {
  const warn = [];
  if (w.dirty) warn.push('uncommitted changes');
  if (w.aheadOrigin > 0) warn.push(`${w.aheadOrigin} unpushed commit${w.aheadOrigin === 1 ? '' : 's'}`);
  let bodyHTML = `Delete worktree <b class="text-app">${w.name}</b> and remove its folder from disk?`;
  if (warn.length) {
    bodyHTML = `<div class="rounded-md border-l-2 px-2.5 py-2" style="border-color:var(--red);background:color-mix(in srgb,var(--red) 10%,transparent)">`
      + `<div class="font-semibold text-app">⚠ ${w.name} has ${warn.join(' and ')}.</div>`
      + `<div class="mt-0.5" style="color:var(--red)">This work will be permanently lost.</div></div>`;
  }
  if (!(await confirmModal('Delete worktree', bodyHTML, { danger: true, confirmLabel: 'Delete worktree' }))) return;
  try {
    // stop any sessions running in this worktree first
    for (const s of state.sessions.filter((x) => x.cwd === w.path)) {
      try { await api('DELETE', `/api/sessions/${s.id}`); } catch (_) {}
      const rec = state.terms[s.id];
      if (rec) { if (rec.ws) rec.ws.close(); rec.term.dispose(); rec.host.remove(); delete state.terms[s.id]; }
    }
    await api('DELETE', '/api/worktrees', { path: w.path, force: true });
    if (state.selected === w.path) state.selected = state.info.repoDir;
    await loadWorktrees(); await loadSessions();
    selectWorktree(state.selected);
    toast(`Deleted worktree ${w.name}`, 'ok');
  } catch (e) { toast(e.message); }
}

// ---- wiring ---------------------------------------------------------------

function isMobile() { return window.innerWidth < 768; }
function openSidebar() { $('#sidebar').classList.remove('-translate-x-full'); $('#backdrop').classList.remove('hidden'); }
function closeSidebar() { $('#sidebar').classList.add('-translate-x-full'); $('#backdrop').classList.add('hidden'); }
function maybeCloseSidebar() { if (isMobile()) closeSidebar(); }

// ---- theme (system / light / dark) ---------------------------------------

function currentTheme() { return localStorage.getItem('shellraiser-theme') || 'system'; }
function applyTheme(t) {
  const dark = t === 'dark' || (t === 'system' && matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.classList.toggle('dark', dark);
  document.querySelectorAll('[data-theme]').forEach((b) => b.classList.toggle('active', b.dataset.theme === t));
}
function setTheme(t) { localStorage.setItem('shellraiser-theme', t); applyTheme(t); }

function wire() {
  $('#new-worktree').onclick = newWorktree;
  $('#menu-btn').onclick = () => ($('#sidebar').classList.contains('-translate-x-full') ? openSidebar() : closeSidebar());
  $('#backdrop').onclick = closeSidebar;
  $('#add-tab').onclick = (e) => { e.stopPropagation(); $('#launch-menu').classList.toggle('hidden'); };
  document.addEventListener('click', () => $('#launch-menu').classList.add('hidden'));
  document.querySelectorAll('[data-theme]').forEach((b) => { b.onclick = () => setTheme(b.dataset.theme); });
  applyTheme(currentTheme());
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => { if (currentTheme() === 'system') applyTheme('system'); });
  // Mobile keyboard chrome.
  buildKeyBar();
  $('#kb-fab').onclick = () => focusActiveTerm();
  if (window.visualViewport) window.visualViewport.addEventListener('resize', () => { const t = state.terms[state.active]; if (t) fit(t); });
  window.addEventListener('resize', () => { const t = state.terms[state.active]; if (t) fit(t); updateMobileChrome(); });
  // Reflect branch/worktree changes made outside the UI when you return to it.
  window.addEventListener('focus', () => { loadWorktrees().catch(() => {}); loadSessions().catch(() => {}); loadPorts().catch(() => {}); });
  // Quick filter across worktrees.
  $('#wt-filter').oninput = (e) => { state.wtFilter = e.target.value; renderWorktrees(); };
  // Collapse/expand the projects rail (persisted), for context switching.
  const psec = $('#projects-section');
  if (localStorage.getItem('sr-projects-collapsed') === '1') psec.classList.add('collapsed');
  $('#projects-toggle').onclick = () => {
    psec.classList.toggle('collapsed');
    localStorage.setItem('sr-projects-collapsed', psec.classList.contains('collapsed') ? '1' : '0');
  };
}

// ---- password auth --------------------------------------------------------

function loginErr(msg) {
  const box = $('#login-err');
  box.textContent = msg;
  box.classList.remove('hidden');
}

function showLoginOverlay() {
  $('#app').style.display = 'none';
  const o = $('#login');
  o.classList.remove('hidden'); o.classList.add('flex');
}

function showSignin() {
  $('#login-signin').classList.remove('hidden');
  $('#login-setpw').classList.add('hidden');
  const pw = $('#login-pw');
  setTimeout(() => pw.focus(), 0);
  const submit = async () => {
    $('#login-err').classList.add('hidden');
    try {
      const r = await capi('POST', '/api/auth/login', { password: pw.value });
      if (r.mustSetPassword) { showSetPassword('Pick a password to replace the one-time code.'); return; }
      location.reload();
    } catch (e) { loginErr(e.message || 'sign in failed'); }
  };
  $('#login-btn').onclick = submit;
  pw.onkeydown = (e) => { if (e.key === 'Enter') submit(); };
}

function showSetPassword(hint) {
  $('#login-signin').classList.add('hidden');
  $('#login-setpw').classList.remove('hidden');
  $('#setpw-hint').textContent = hint || '';
  const a = $('#setpw-1'), b = $('#setpw-2');
  a.value = ''; b.value = '';
  setTimeout(() => a.focus(), 0);
  const submit = async () => {
    $('#login-err').classList.add('hidden');
    if (a.value.length < 6) { loginErr('Password must be at least 6 characters.'); return; }
    if (a.value !== b.value) { loginErr('Passwords do not match.'); return; }
    try { await capi('POST', '/api/auth/password', { password: a.value }); location.reload(); }
    catch (e) { loginErr(e.message || 'could not set password'); }
  };
  $('#setpw-btn').onclick = submit;
  b.onkeydown = (e) => { if (e.key === 'Enter') submit(); };
}

// Settings modal (signed in): change password + global passthrough toggles.
async function openSettings() {
  let cfg = { sshPassthrough: false, gitPassthrough: false };
  try { cfg = await capi('GET', '/api/config'); } catch (_) {}
  const v = await modal({
    title: 'Settings',
    bodyHTML: '<div class="text-muted">Change the password, and choose whether new workers can use your host SSH agent (YubiKey) and git config. Passthrough exposes those to the sandbox — enable only if you trust what runs there.</div>',
    fields: [
      { name: 'ssh', label: 'Forward host SSH agent + ~/.ssh into workers', type: 'checkbox', value: cfg.sshPassthrough },
      { name: 'git', label: 'Bind host ~/.gitconfig into workers', type: 'checkbox', value: cfg.gitPassthrough },
      { name: 'p1', label: 'New password (blank = unchanged)', type: 'password' },
      { name: 'p2', label: 'Confirm', type: 'password' },
    ],
    actions: [{ label: 'Cancel', value: null }, { label: 'Save', primary: true }],
  });
  if (!v) return;
  try {
    await capi('POST', '/api/config', { sshPassthrough: v.ssh, gitPassthrough: v.git });
  } catch (e) { toast(e.message); return; }
  if (v.p1) {
    if (v.p1.length < 6) { toast('Password must be at least 6 characters'); return; }
    if (v.p1 !== v.p2) { toast('Passwords do not match'); return; }
    try { await capi('POST', '/api/auth/password', { password: v.p1 }); } catch (e) { toast(e.message); return; }
  }
  toast('Settings saved (passthrough applies to newly-started workers)', 'ok');
}

async function initApp(authEnabled) {
  wire();
  if (authEnabled) {
    $('#settings-btn').classList.remove('hidden');
    $('#settings-btn').onclick = openSettings;
    $('#logout').classList.remove('hidden');
    $('#logout').onclick = async () => { await capi('POST', '/api/auth/logout', {}); location.reload(); };
  }

  await loadProjects();

  // Coordinator root (no project selected): jump to the first project, or show
  // the empty state when nothing is registered yet.
  if (!state.projectId) {
    if (state.projects.length) { location.replace('/w/' + state.projects[0].id + '/'); return; }
    $('#repo-name').textContent = 'no projects';
    $('#empty-state').style.display = 'flex';
    $('#empty-state').textContent = 'Run `sr` in a git repository to add a project.';
    setInterval(loadProjects, 5000);
    return;
  }

  await loadInfo();
  await loadCommands();
  await loadWorktrees();
  await loadSessions();
  await loadPorts();
  connectEvents();
  setInterval(loadPorts, 5000);
  setInterval(loadWorktrees, 15000); // refresh git stats
  setInterval(loadProjects, 10000);  // keep the project rail fresh
}

async function main() {
  fillIcons();
  applyTheme(currentTheme());
  let status = { enabled: false, authenticated: true, mustSetPassword: false };
  try { status = await capi('GET', '/api/auth/status'); } catch (_) {}
  if (status.enabled && !status.authenticated) {
    showLoginOverlay();
    showSignin();
    return;
  }
  if (status.enabled && status.authenticated && status.mustSetPassword) {
    showLoginOverlay();
    showSetPassword('Set a password to finish securing this coordinator.');
    return;
  }
  await initApp(status.enabled);
}

main().catch((e) => toast('startup: ' + e.message));

// slopbox front-end: worktree nav, tabbed xterm sessions, live status + ding.
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
  commands: [],          // custom launchers from .slopbox.toml
  active: null,          // active tab id
  ports: [],             // [{port,process,worktree,sessionId}]
  terms: {},             // id -> { term, fit, ws, host }
  audio: null,
  ctrlSticky: false,     // mobile key-bar Ctrl modifier armed for next key
};

window.__slopbox = state; // exposed for automated tests
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
  { kind: 'editor', icon: 'pencil', label: 'editor' },
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
  for (const b of BUILTIN_LAUNCH) menu.appendChild(item(b.icon, b.label, () => launch(b.kind)));
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

function renderWorktrees() {
  const nav = $('#worktrees');
  nav.innerHTML = '';
  for (const w of state.worktrees) {
    const sel = w.path === state.selected;
    const row = el('div', `group relative mb-0.5 cursor-pointer rounded-md px-2 py-1.5 transition ${sel ? 'row-sel' : 'hover-row'}`);
    if (w.color) row.style.borderLeft = `3px solid ${w.color}`;
    row.style.paddingLeft = w.color ? '0.4rem' : '';
    const top = el('div', 'flex items-center gap-2');
    const ic = el('span', ''); ic.style.color = w.color || cssVar('--faint'); ic.innerHTML = svg('branch', 'icon icon-sm'); top.appendChild(ic);
    const name = el('span', 'truncate text-sm ' + (sel ? 'text-app font-medium' : 'text-muted'), w.displayName || w.name);
    if (w.displayName) name.title = `${w.name} · ${w.branch || ''}`;
    top.appendChild(name);
    top.appendChild(gitBadge(w));
    // hover actions: color · rename · open-in-editor · delete
    const actions = el('span', 'ml-1 flex shrink-0 items-center gap-0.5 opacity-0 transition group-hover:opacity-100');
    const colorBtn = el('button', 'iconbtn px-1'); colorBtn.title = 'Color';
    colorBtn.innerHTML = `<span class="inline-block h-3 w-3 rounded-full" style="background:${w.color || 'transparent'};box-shadow:inset 0 0 0 1px var(--border)"></span>`;
    colorBtn.onclick = (ev) => { ev.stopPropagation(); openColorPicker(w, colorBtn); };
    actions.appendChild(colorBtn);
    const ren = el('button', 'iconbtn px-1'); ren.title = 'Rename'; ren.innerHTML = svg('pencil', 'icon icon-sm');
    ren.onclick = (ev) => { ev.stopPropagation(); renameWorktree(w); };
    actions.appendChild(ren);
    if (state.info && state.info.editor) {
      const edit = el('button', 'iconbtn px-1'); edit.title = 'Open in code-server'; edit.innerHTML = svg('external', 'icon icon-sm');
      edit.onclick = (ev) => { ev.stopPropagation(); window.open(BASE + '/edit/?folder=' + encodeURIComponent(w.path), '_blank'); };
      actions.appendChild(edit);
    }
    if (!w.isMain) {
      const del = el('button', 'iconbtn px-1'); del.title = 'Delete worktree'; del.innerHTML = svg('trash', 'icon icon-sm');
      del.onclick = (ev) => { ev.stopPropagation(); deleteWorktree(w); };
      actions.appendChild(del);
    }
    top.appendChild(actions);
    row.appendChild(top);
    const branch = el('div', 'truncate pl-6 text-[11px] text-faint', w.detached ? `detached @ ${(w.head || '').slice(0, 7)}` : (w.branch || ''));
    row.appendChild(branch);
    const meta = gitMeta(w);
    if (meta) { const m = el('div', 'flex flex-wrap items-center gap-1.5 pl-6 pt-0.5 text-[11px]'); m.innerHTML = meta; row.appendChild(m); }
    row.onclick = () => selectWorktree(w.path);
    row.ondblclick = () => maybeCloseSidebar();

    // nested sessions for this worktree
    const kids = state.sessions.filter((s) => s.cwd === w.path);
    for (const s of kids) {
      const k = el('div', 'hover-row mt-0.5 ml-4 flex items-center gap-2 rounded px-2 py-1 text-xs');
      k.appendChild(dot(s.state));
      const ki = el('span', 'text-muted'); ki.innerHTML = svg(KIND[s.kind] || 'play', 'icon icon-sm'); k.appendChild(ki);
      k.appendChild(el('span', 'truncate text-app', s.title));
      k.onclick = (ev) => { ev.stopPropagation(); openTab(s); };
      row.appendChild(k);
    }
    nav.appendChild(row);
  }
}

function renderContext() {
  const w = state.worktrees.find((x) => x.path === state.selected);
  const box = $('#active-context');
  box.innerHTML = '';
  if (!w) { box.appendChild(el('span', 'text-muted', 'Select a worktree')); return; }
  const ic = el('span', 'text-accent'); ic.innerHTML = svg('branch', 'icon icon-sm'); box.appendChild(ic);
  box.appendChild(el('span', 'font-semibold text-app', w.displayName || w.name));
  box.appendChild(el('span', 'text-faint', '·'));
  box.appendChild(el('span', 'truncate text-muted', w.detached ? 'detached' : (w.branch || '')));
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

function portChip(p) {
  // Each port: an /p/ HTTP-proxy link plus a map toggle. Mapping opens a real
  // host-loopback bind (SSH -L), so non-HTTP services work too — green when live.
  const host = state.portMaps[p.port];
  const wrap = el('div', 'flex items-center gap-0.5');
  const a = el('a', 'btn flex-1 px-1.5 py-0.5 text-center text-[11px]');
  a.textContent = p.port;
  a.title = `${p.process ? p.process + ' ' : ''}→ /p/${p.port}/ (HTTP proxy)`;
  a.href = `${BASE}/p/${p.port}/`;
  a.target = '_blank';
  wrap.appendChild(a);
  const map = el('button', 'iconbtn px-1');
  map.innerHTML = svg('external', 'icon icon-sm');
  if (host) { map.classList.add('text-accent'); map.title = `mapped → 127.0.0.1:${host} (click to unmap)`; map.style.color = 'var(--green)'; }
  else { map.title = 'Map to a host-loopback port (SSH tunnel)'; }
  map.onclick = (ev) => { ev.stopPropagation(); host ? unmapPort(p.port) : mapPort(p.port); };
  wrap.appendChild(map);
  return wrap;
}

async function loadPortMaps() {
  if (!state.projectId) return;
  try {
    const list = await capi('GET', `/api/workers/${state.projectId}/ports`);
    state.portMaps = Object.fromEntries((list || []).map((m) => [m.container, m.host]));
  } catch (_) { state.portMaps = {}; }
}

async function mapPort(port) {
  try {
    const r = await capi('POST', `/api/workers/${state.projectId}/ports/${port}/map`, {});
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
  // Scoped to the selected worktree: only ports owned by a process running there.
  const box = $('#ports');
  box.innerHTML = '';
  const mine = state.ports.filter((p) => p.worktree && p.worktree === state.selected);
  $('#ports-count').textContent = mine.length ? mine.length : '';
  if (!mine.length) { box.appendChild(el('div', 'col-span-3 text-faint', 'none')); return; }
  for (const p of mine) box.appendChild(portChip(p));
}

// ---- sessions & terminals -------------------------------------------------

async function launch(kind) {
  if (!state.selected) { toast('Pick a worktree first'); return; }
  unlockAudio();
  try {
    const s = await api('POST', '/api/sessions', { kind, cwd: state.selected });
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
  titleTimer = setTimeout(() => { document.title = 'slopbox'; }, 5000);
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
  pop.style.left = `${r.left}px`; pop.style.top = `${r.bottom + 4}px`;
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

function currentTheme() { return localStorage.getItem('slopbox-theme') || 'system'; }
function applyTheme(t) {
  const dark = t === 'dark' || (t === 'system' && matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.classList.toggle('dark', dark);
  document.querySelectorAll('[data-theme]').forEach((b) => b.classList.toggle('active', b.dataset.theme === t));
}
function setTheme(t) { localStorage.setItem('slopbox-theme', t); applyTheme(t); }

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
}

// ---- passkey auth ---------------------------------------------------------

const SWA = window.SimpleWebAuthnBrowser;

async function passkeyRegister(code, label) {
  const opts = await capi('POST', '/api/auth/register/begin', { code, label });
  const att = await SWA.startRegistration({ optionsJSON: opts.publicKey });
  await capi('POST', '/api/auth/register/finish', att);
}

async function passkeyLogin() {
  const opts = await capi('POST', '/api/auth/login/begin', {});
  const asr = await SWA.startAuthentication({ optionsJSON: opts.publicKey });
  await capi('POST', '/api/auth/login/finish', asr);
}

function loginErr(e) {
  const box = $('#login-err');
  box.textContent = e.message || String(e);
  box.classList.remove('hidden');
}

function showLogin(status) {
  $('#app').style.display = 'none';
  const overlay = $('#login');
  overlay.classList.remove('hidden');
  overlay.classList.add('flex');
  $('#login-rp').textContent = status.rpId || '';
  if (status.registered) {
    // A passkey already exists for this host — just offer sign-in. The
    // bootstrap/register box is only for the first passkey on a new host.
    $('#login-signin').classList.remove('hidden');
    $('#login-register').classList.add('hidden');
    $('#passkey-login').onclick = async () => { try { await passkeyLogin(); location.reload(); } catch (e) { loginErr(e); } };
  } else {
    $('#login-signin').classList.add('hidden');
    $('#login-register').classList.remove('hidden');
    $('#passkey-register').onclick = async () => {
      try { await passkeyRegister($('#reg-code').value.trim(), $('#reg-label').value.trim() || 'passkey'); location.reload(); }
      catch (e) { loginErr(e); }
    };
  }
}

// Add an additional passkey while already signed in (no bootstrap code needed).
async function addPasskey() {
  const label = await promptModal('Add a passkey', { name: 'label', label: 'Label', placeholder: 'phone' }, 'Add');
  if (label === null) return;
  try { await passkeyRegister('', label || 'passkey'); toast('Passkey added', 'ok'); }
  catch (e) { toast(e.message); }
}

async function initApp(authEnabled) {
  wire();
  if (authEnabled) {
    $('#add-passkey').classList.remove('hidden');
    $('#add-passkey').onclick = addPasskey;
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
    $('#empty-state').textContent = 'Run `sb` in a git repository to add a project.';
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
  let status = { enabled: false, authenticated: true };
  try { status = await capi('GET', '/api/auth/status'); } catch (_) {}
  if (status.enabled && !status.authenticated) {
    showLogin(status);
    return;
  }
  await initApp(status.enabled);
}

main().catch((e) => toast('startup: ' + e.message));

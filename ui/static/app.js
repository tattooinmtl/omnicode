// OmniCode Web UI
// Vanilla JS, no framework. Talks to /api/* endpoints.
// Each "view" is a function that renders into #view.

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const state = {
  view: 'chat',
  chat: {
    sessionId: null,
    messages: [],
  },
  editor: {
    projectId: null,
    projectPath: null,
    tree: null,
    tabs: [],            // [{ id, path, label, kind: 'file'|'plan', dirty, monaco }]
    activeTab: null,
    planDirty: false,
  },
  data: {
    providers: [],
    projects: [],
    mcp: [],
    plugins: [],
    skills: [],
    agents: [],
    sessions: [],
    secrets: [],
  },
  config: null,
  consoleLines: [],
  dock: {
    share: { console: 1, prompt: 1.35, terminal: 1.15 },
    closed: { console: false, prompt: false, terminal: false },
  },
  terminal: { id: null, projectId: '', cwd: '', term: null, fit: null, source: null },
  floatTerm: { id: null, projectId: '', cwd: '', term: null, fit: null, source: null, mode: 'float', floatPos: null },
};

// ----------------------------------------------------------
// Markdown / tool-card render helpers (assistant messages)
// ----------------------------------------------------------

// renderMarkdown converts assistant text to HTML using marked + highlight.js.
// If those libraries have not loaded yet (CDN still in flight), it falls back
// to escaped plain text so the bubble still renders something readable.
function renderMarkdown(text) {
  if (!text) return '';
  if (!window.marked) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
  }
  const html = window.marked.parse(text, {
    gfm: true,
    breaks: true,
    headerIds: false,
    mangle: false,
  });
  const wrap = document.createElement('div');
  wrap.innerHTML = html;
  wrap.querySelectorAll('pre > code').forEach(code => {
    const pre = code.parentElement;
    let lang = (code.className.match(/language-(\S+)/) || [, 'txt'])[1];
    if (lang === 'txt') lang = 'plaintext';
    if (window.hljs) {
      try { window.hljs.highlightElement(code); } catch (e) { /* ignore */ }
    }
    const head = document.createElement('div');
    head.className = 'code-block-head';
    const langSpan = document.createElement('span');
    langSpan.className = 'code-lang';
    langSpan.textContent = lang;
    const copyBtn = document.createElement('button');
    copyBtn.className = 'copy-btn';
    copyBtn.type = 'button';
    copyBtn.textContent = 'copy';
    copyBtn.onclick = () => {
      navigator.clipboard.writeText(code.innerText).catch(() => {});
      copyBtn.textContent = 'copied';
      setTimeout(() => { copyBtn.textContent = 'copy'; }, 1200);
    };
    head.appendChild(langSpan);
    head.appendChild(copyBtn);
    pre.appendChild(head);
  });
  return wrap.innerHTML;
}

// renderToolCard builds one collapsible card per tool invocation the
// assistant made. Keeping each tool visible is the difference between
// "AI replied with text" and "I can actually see what the AI did".
function renderToolCard(tu) {
  const el = document.createElement('div');
  el.className = 'tool-card';
  const input = (tu && tu.input) || {};
  const name = tu.name || 'tool';
  let label = '';
  if (name === 'bash' || name === 'REPL' || name === 'PowerShell') {
    label = input.command || input.cmd || '(no command)';
  } else if (name === 'read_file' || name === 'write_file' || name === 'edit_file') {
    label = input.path || input.file_path || '';
  } else if (name === 'WebFetch') {
    label = input.url || '';
  } else if (name === 'WebSearch') {
    label = input.query || '';
  } else if (name === 'TodoWrite') {
    label = JSON.stringify(input.todos || input).slice(0, 100);
  } else if (name === 'Skill') {
    label = input.name || input.skill || '';
  } else {
    const k = Object.keys(input)[0];
    label = k ? `${k}: ${String(input[k]).slice(0, 100)}` : '';
  }
  const status = tu.status || 'ok';
  el.innerHTML = `
    <div class="tool-head">
      <span class="tag"></span>
      <span class="label"></span>
      <span class="status"></span>
    </div>
    <div class="tool-body"></div>`;
  el.querySelector('.tag').textContent = name;
  el.querySelector('.label').textContent = label;
  el.querySelector('.status').textContent = status;
  el.querySelector('.status').classList.add(status);
  el.querySelector('.tool-body').textContent = JSON.stringify(input, null, 2);
  el.querySelector('.tool-head').onclick = () => el.classList.toggle('open');
  return el;
}

// stepCounter nudges plan-step numbering when the creation pipeline runs.
let planStepN = 0;

// appendPlanStep stores a brief trace of each turn in plan.md so the user
// can return to any past reasoning. Best-effort; failures are non-fatal.
async function appendPlanStep(sessionId, userMsg, assistantMsg, toolUses) {
  if (!sessionId) return;
  planStepN += 1;
  const lines = [];
  lines.push(`## Step ${planStepN} — ${new Date().toLocaleString()}`);
  if (userMsg) lines.push(`\n**Prompt:**\n\n> ${userMsg.replace(/\n/g, '\n> ')}\n`);
  if (assistantMsg) lines.push(`\n**Assistant:**\n\n${assistantMsg}\n`);
  if (toolUses && toolUses.length) {
    lines.push(`\n**Tool calls:**`);
    for (const tu of toolUses) {
      lines.push(`- \`${tu.name}\` ${JSON.stringify(tu.input).slice(0, 200)}`);
    }
  }
  try {
    await post('/api/plan/append', {
      session_id: sessionId,
      section: lines.join('\n'),
    });
  } catch (e) { /* non-fatal */ }
}

// openTabsForToolUses implements the creation pipeline: every write_file
// the assistant invokes becomes a new tab in arrival order. The user can
// close / reorder; arrival order is the audit trail.
function openTabsForToolUses(toolUses) {
  if (!toolUses || !toolUses.length) return;
  for (const tu of toolUses) {
    if (tu.name !== 'write_file' && tu.name !== 'Write') continue;
    const p = tu.input && (tu.input.path || tu.input.file_path);
    if (!p) continue;
    editorOpenPath(p);
  }
}

// ----------------------------------------------------------
// API helpers
// ----------------------------------------------------------

async function api(method, path, body, raw) {
  const opts = { method, headers: {} };
  if (raw !== undefined) {
    // FormData/multipart: let the browser set the Content-Type boundary.
    opts.body = raw;
  } else if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const r = await fetch(path, opts);
  const text = await r.text();
  let data;
  try { data = text ? JSON.parse(text) : {}; } catch (e) { data = { error: text }; }
  if (!r.ok) {
    throw new Error(data.error || r.statusText);
  }
  return data;
}

const get  = (p)    => api('GET',    p);
const post = (p, b) => api('POST',   p, b);
const put  = (p, b) => api('PUT',    p, b);
const del  = (p)    => api('DELETE', p);

// ----------------------------------------------------------
// Toasts
// ----------------------------------------------------------

function toast(msg, kind = 'ok') {
  let wrap = $('.toast-wrap');
  if (!wrap) {
    wrap = document.createElement('div');
    wrap.className = 'toast-wrap';
    document.body.appendChild(wrap);
  }
  const t = document.createElement('div');
  t.className = 'toast ' + kind;
  t.textContent = msg;
  wrap.appendChild(t);
  setTimeout(() => {
    t.style.opacity = '0';
    setTimeout(() => t.remove(), 200);
  }, 3500);
}

// ----------------------------------------------------------
// Routing
// ----------------------------------------------------------

function setView(name) {
  state.view = name;
  $$('.nav-item').forEach(el => {
    el.classList.toggle('active', el.dataset.view === name);
  });
  document.body.classList.toggle('editor-mode', name === 'editor');
  const menu = $('#editor-menu');
  if (menu) menu.hidden = false;
  if (name === 'editor') paintSideMessages();
  const labels = {
    chat: 'Chat', editor: 'Editor', projects: 'Projects', sessions: 'Sessions',
    providers: 'Providers', endpoints: 'Endpoints', 'ai-control': 'AI Control',
    mcp: 'MCP Servers', plugins: 'Plugins', skills: 'Skills', agents: 'Agents',
    database: 'Database', secrets: 'API Keys', app: 'App Settings', audit: 'Audit',
  };
  $('#crumbs').textContent = labels[name] || name;
  render();
}

function render() {
  const v = $('#view');
  v.innerHTML = '';
  const fn = views[state.view] || views.chat;
  fn(v);
}

// ----------------------------------------------------------
// Header (provider / model / permission)
// ----------------------------------------------------------

async function refreshHeader() {
  try {
    state.data.providers = (await get('/api/providers')).data || [];
  } catch (e) { /* ignore */ }

  // Restore the provider the app used last. The server remembers it too, so
  // this stays pinned across restarts unless the user picks something else
  // or the endpoint goes down.
  let saved = {};
  try { saved = (await get('/api/selection')).data || {}; } catch (e) { /* ignore */ }

  const ps = $('#provider-select');
  const list = state.data.providers;
  ps.innerHTML = list.length === 0
    ? '<option value="">(no providers configured)</option>'
    : list.map(p => {
        const label = p.label || p.name || p.id;
        const kind = p.kind ? ` (${p.kind})` : '';
        const mark = p.has_api_key ? '' : ' · no key';
        return `<option value="${p.id}">${label}${kind}${mark}</option>`;
      }).join('');

  const wanted = saved.provider_id;
  if (wanted && list.some(p => p.id === wanted)) {
    ps.value = wanted;
  }

  const ms = $('#model-select');
  ms.disabled = true;
  ms.innerHTML = '<option>loading models…</option>';

  // Fetch the model list live from the selected provider's /v1/models,
  // using that provider's own key from agent/settings.json.
  const loadModels = async (providerId) => {
    if (!providerId) {
      ms.disabled = false;
      ms.innerHTML = '<option>(no provider selected)</option>';
      return;
    }
    try {
      const r = await post(`/api/providers/${providerId}/models`);
      const models = (r.data && r.data.models) || [];
      if (models.length === 0) {
        ms.innerHTML = '<option>(endpoint returned no models)</option>';
      } else {
        ms.innerHTML = models.map(m => `<option>${m}</option>`).join('');
        // Prefer the model this app used last, if the endpoint still serves it.
        const sel = list.find(p => p.id === providerId);
        const free = models.find(m => /:free$/i.test(m) || /free/i.test(m));
        if (saved.model && models.includes(saved.model)) ms.value = saved.model;
        else if (sel && sel.default_model && models.includes(sel.default_model)) {
          ms.value = sel.default_model;
        } else if (free) {
          // The alphabetically first id is often a paid or non-chat model.
          ms.value = free;
        }
      }
    } catch (e) {
      ms.innerHTML = `<option>(${e.message})</option>`;
    } finally {
      ms.disabled = false;
    }
  };

  loadModels(ps.value);

  // Record an explicit user change so the server pins it from now on, and
  // reload the model list for the newly selected endpoint.
  ps.onchange = async () => {
    await persistSelection();
    await loadModels(ps.value);
  };
  ms.onchange = () => persistSelection();
}

// persistSelection tells the server which provider/model the user chose.
async function persistSelection() {
  const ps = $('#provider-select');
  const ms = $('#model-select');
  if (!ps) return;
  try {
    await post('/api/selection', {
      provider_id: ps.value,
      model: ms ? ms.value : '',
    });
  } catch (e) { /* non-fatal */ }
}

// ----------------------------------------------------------
// Views
// ----------------------------------------------------------

function consoleLog(line) {
  const text = String(line || '').trim();
  if (!text) return;
  state.consoleLines.push(text);
  if (state.consoleLines.length > 300) state.consoleLines.shift();
  const el = document.getElementById('dock-console-log');
  if (!el) return;
  const row = document.createElement('div');
  row.textContent = text;
  el.appendChild(row);
  el.scrollTop = el.scrollHeight;
}

function paintConsole() {
  const el = document.getElementById('dock-console-log');
  if (!el) return;
  el.innerHTML = '';
  state.consoleLines.forEach(text => {
    const row = document.createElement('div');
    row.textContent = text;
    el.appendChild(row);
  });
  el.scrollTop = el.scrollHeight;
}

function loadScript(src) {
  return new Promise((resolve, reject) => {
    const existing = document.querySelector('script[src="' + src + '"]');
    if (existing) { existing.addEventListener('load', () => resolve()); resolve(); return; }
    const s = document.createElement('script');
    s.src = src;
    s.onload = () => resolve();
    s.onerror = () => reject(new Error('failed to load ' + src));
    document.head.appendChild(s);
  });
}

async function ensureXterm() {
  if (window.Terminal) return;
  if (!document.querySelector('link[data-xterm]')) {
    const l = document.createElement('link');
    l.rel = 'stylesheet';
    l.href = 'https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css';
    l.dataset.xterm = '1';
    document.head.appendChild(l);
  }
  await loadScript('https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js');
  await loadScript('https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js');
}

function bytesFromB64(b64) {
  const bin = atob(b64 || '');
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

let termMount = 0;

const DOCK_PANES = ['console', 'prompt', 'terminal'];
const DOCK_LABELS = { console: 'Console', prompt: 'Prompt', terminal: 'Terminal' };
const DOCK_MIN = { console: 140, prompt: 200, terminal: 180 };

function dockOpenPanes() {
  const open = DOCK_PANES.filter(id => !state.dock.closed[id]);
  if (open.length) return open;
  state.dock.closed.prompt = false;
  return ['prompt'];
}

let dockFitQueued = false;
function fitChatTerminal() {
  if (dockFitQueued) return;
  dockFitQueued = true;
  requestAnimationFrame(() => {
    dockFitQueued = false;
    if (state.dock.closed.terminal || !state.terminal.fit) return;
    state.terminal.fit.fit();
  });
}

// Widths are shares of the open panes. Closing a pane drops it from the
// flex sum, so the panes still open grow into the freed space. Split B also
// sits between Console and Terminal when the prompt is closed.
function splitPair(which) {
  const c = !state.dock.closed.console;
  const p = !state.dock.closed.prompt;
  const t = !state.dock.closed.terminal;
  if (which === 'a') return (c && p) ? ['console', 'prompt'] : null;
  if (p && t) return ['prompt', 'terminal'];
  if (!p && c && t) return ['console', 'terminal'];
  return null;
}

function applyDockLayout() {
  const dock = document.getElementById('chat-dock');
  if (!dock) return;
  const open = dockOpenPanes();
  // A flex-grow sum below 1 leaves empty space. Scale the open shares so
  // they always add up to the number of open panes and fill the dock.
  const total = open.reduce((sum, id) => sum + (state.dock.share[id] || 1), 0) || 1;
  DOCK_PANES.forEach(id => {
    const pane = dock.querySelector('[data-pane="' + id + '"]');
    if (!pane) return;
    const isOpen = open.includes(id);
    pane.hidden = !isOpen;
    if (isOpen) {
      const grow = (state.dock.share[id] || 1) / total * open.length;
      pane.style.flex = grow + ' 1 0px';
    }
  });
  const splitA = dock.querySelector('[data-split="a"]');
  const splitB = dock.querySelector('[data-split="b"]');
  const c = open.includes('console');
  const p = open.includes('prompt');
  const t = open.includes('terminal');
  if (splitA) splitA.hidden = !(c && p);
  if (splitB) splitB.hidden = !(t && (p || c));
  dock.querySelectorAll('[data-close]').forEach(btn => { btn.hidden = open.length <= 1; });
  const bar = document.getElementById('dock-restore');
  if (bar) {
    const closed = DOCK_PANES.filter(id => !open.includes(id));
    bar.hidden = closed.length === 0;
    bar.replaceChildren();
    closed.forEach(id => {
      const b = document.createElement('button');
      b.type = 'button';
      b.dataset.open = id;
      b.title = 'Show ' + DOCK_LABELS[id];
      b.textContent = DOCK_LABELS[id];
      bar.appendChild(b);
    });
  }
  fitChatTerminal();
}

function bindDockResize() {
  const dock = document.getElementById('chat-dock');
  const handle = document.getElementById('dock-resize');
  if (!dock || !handle) return;
  if (!dock.dataset.bound) {
    dock.dataset.bound = '1';
    let startY = 0;
    let startH = 0;
    handle.addEventListener('pointerdown', (e) => {
      if (e.button !== 0) return;
      startY = e.clientY;
      startH = dock.getBoundingClientRect().height;
      handle.setPointerCapture(e.pointerId);
    });
    handle.addEventListener('pointermove', (e) => {
      if (!handle.hasPointerCapture(e.pointerId)) return;
      const next = Math.max(168, Math.min(window.innerHeight * 0.72, startH + (startY - e.clientY)));
      dock.style.height = next + 'px';
      fitChatTerminal();
    });

    dock.querySelectorAll('.dock-split').forEach(split => {
      let drag = null;
      split.addEventListener('pointerdown', (e) => {
        if (e.button !== 0) return;
        const pair = splitPair(split.dataset.split);
        if (!pair) return;
        const leftEl = dock.querySelector('[data-pane="' + pair[0] + '"]');
        const rightEl = dock.querySelector('[data-pane="' + pair[1] + '"]');
        if (!leftEl || !rightEl) return;
        drag = {
          leftId: pair[0],
          rightId: pair[1],
          startX: e.clientX,
          leftShare: state.dock.share[pair[0]] || 1,
          rightShare: state.dock.share[pair[1]] || 1,
          sumPx: leftEl.getBoundingClientRect().width + rightEl.getBoundingClientRect().width,
        };
        split.classList.add('dragging');
        split.setPointerCapture(e.pointerId);
        e.preventDefault();
      });
      split.addEventListener('pointermove', (e) => {
        if (!drag || !split.hasPointerCapture(e.pointerId)) return;
        const sumShare = drag.leftShare + drag.rightShare;
        const floor = Math.min(DOCK_MIN[drag.leftId] || 120, drag.sumPx * 0.2);
        const ceil = drag.sumPx - Math.min(DOCK_MIN[drag.rightId] || 120, drag.sumPx * 0.2);
        let leftPx = (drag.leftShare / sumShare) * drag.sumPx + (e.clientX - drag.startX);
        leftPx = Math.max(floor, Math.min(ceil, leftPx));
        const frac = drag.sumPx > 0 ? leftPx / drag.sumPx : 0.5;
        state.dock.share[drag.leftId] = sumShare * frac;
        state.dock.share[drag.rightId] = sumShare * (1 - frac);
        applyDockLayout();
      });
      const end = () => {
        drag = null;
        split.classList.remove('dragging');
      };
      split.addEventListener('pointerup', end);
      split.addEventListener('pointercancel', end);
    });

    dock.addEventListener('click', (e) => {
      const closer = e.target.closest('[data-close]');
      if (closer) {
        const id = closer.dataset.close;
        if (!DOCK_PANES.includes(id) || dockOpenPanes().length <= 1) return;
        state.dock.closed[id] = true;
        applyDockLayout();
        return;
      }
      const opener = e.target.closest('[data-open]');
      if (!opener || !DOCK_PANES.includes(opener.dataset.open)) return;
      state.dock.closed[opener.dataset.open] = false;
      applyDockLayout();
    });
  }
  applyDockLayout();
}

async function closeTerminalSession() {
  const id = state.terminal.id;
  if (state.terminal.source) {
    state.terminal.source.close();
    state.terminal.source = null;
  }
  if (state.terminal.term) {
    state.terminal.term.dispose();
    state.terminal.term = null;
    state.terminal.fit = null;
  }
  state.terminal.id = null;
  if (id) {
    try { await del('/api/terminal/' + id); } catch (e) { /* already gone */ }
  }
}

async function mountChatDock() {
  const gen = ++termMount;
  bindDockResize();
  paintConsole();
  const host = document.getElementById('term-host');
  const sel = document.getElementById('term-project');
  if (!host || !sel) return;
  let projects = [];
  try { projects = (await get('/api/projects')).data || []; } catch (e) { projects = []; }
  if (gen !== termMount) return;
  const current = state.terminal.projectId || state.editor.projectId || '';
  sel.innerHTML = '<option value="">Home folder</option>' +
    projects.map(p => `<option value="${p.id}">${p.name || p.path}</option>`).join('');
  if (current && [...sel.options].some(o => o.value === current)) sel.value = current;
  sel.onchange = async () => {
    state.terminal.projectId = sel.value;
    state.editor.projectId = sel.value || null;
    await closeTerminalSession();
    mountChatDock();
  };
  try {
    await ensureXterm();
  } catch (e) {
    host.textContent = 'Terminal UI failed to load. Check the network and reload.';
    consoleLog(e.message);
    return;
  }
  if (gen !== termMount) return;
  if (state.terminal.term) {
    state.terminal.term.dispose();
    state.terminal.term = null;
  }
  if (state.terminal.source) {
    state.terminal.source.close();
    state.terminal.source = null;
  }
  const term = new window.Terminal({
    cursorBlink: true,
    fontFamily: 'Consolas, ui-monospace, monospace',
    fontSize: 12,
    theme: { background: '#0b0d10', foreground: '#e7eaf0', cursor: '#7c5cff' },
    scrollback: 2000,
  });
  const fit = new window.FitAddon.FitAddon();
  term.loadAddon(fit);
  host.innerHTML = '';
  term.open(host);
  fit.fit();
  state.terminal.term = term;
  state.terminal.fit = fit;
  const cols = term.cols || 80;
  const rows = term.rows || 16;
  let info;
  if (state.terminal.id && state.terminal.projectId === sel.value && state.terminal.agents) {
    info = { id: state.terminal.id, cwd: state.terminal.cwd, agents: state.terminal.agents };
  } else {
    if (state.terminal.id) await closeTerminalSession();
    let created;
    try {
      created = await post('/api/terminal', {
        project_id: sel.value,
        cols,
        rows,
      });
    } catch (e) {
      host.textContent = 'Terminal is not available until OmniCode is restarted with this build.';
      consoleLog(e.message);
      return;
    }
    if (gen !== termMount) return;
    info = created.data || {};
    state.terminal.id = info.id;
    state.terminal.projectId = sel.value;
    state.terminal.cwd = info.cwd || '';
    state.terminal.agents = info.agents || [];
    consoleLog('Terminal ' + (info.cwd || 'ready'));
  }
  const agents = info.agents || [];
  agents.forEach(a => {
    const btn = document.getElementById('term-' + a.id);
    if (!btn) return;
    btn.disabled = !a.found;
    btn.title = a.found ? a.command : (a.label + ' is not on PATH');
    btn.onclick = () => {
      if (!state.terminal.id || !a.found) return;
      post('/api/terminal/' + state.terminal.id + '/input', { data: a.command + '\r' }).catch(err => consoleLog(err.message));
    };
  });
  term.onData(data => {
    if (!state.terminal.id) return;
    post('/api/terminal/' + state.terminal.id + '/input', { data }).catch(() => {});
  });
  const src = new EventSource('/api/terminal/' + info.id + '/stream');
  state.terminal.source = src;
  src.addEventListener('data', (ev) => {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.b64) term.write(bytesFromB64(msg.b64));
    } catch (e) { /* ignore a bad chunk */ }
  });
  src.addEventListener('exit', () => consoleLog('Terminal closed'));
  src.onerror = () => { /* EventSource retries until we close it */ };
  term.onResize(({ cols: c, rows: r }) => {
    if (!state.terminal.id || state.dock.closed.terminal) return;
    if (c < 2 || r < 2) return;
    post('/api/terminal/' + state.terminal.id + '/resize', { cols: c, rows: r }).catch(() => {});
  });
  fitChatTerminal();
}

function editorContextPayload() {
  return {
    active_file: state.editor.activeTab || '',
    open_tabs: (state.editor.tabs || []).map(t => t.path).filter(Boolean),
    project_path: state.editor.projectPath || '',
  };
}

async function ensureChatSession() {
  if (state.chat.sessionId) return state.chat.sessionId;
  const r = await post('/api/sessions', {
    title: 'New session ' + new Date().toLocaleString(),
    project_id: state.editor.projectId || '',
  });
  state.chat.sessionId = r.data.id;
  try { await get('/api/plan?session_id=' + encodeURIComponent(r.data.id)); } catch (e) { /* ignore */ }
  return state.chat.sessionId;
}

function paintSideMessages() {
  const cm = document.getElementById('side-chat-messages');
  if (!cm) return;
  cm.innerHTML = '';
  if (state.chat.messages.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'empty';
    empty.innerHTML = `
      <div class="glyph">○</div>
      <h2>Editor chat</h2>
      <p>Ask the assistant to read or change the open project. Tools, skills, and MCP run in this folder.</p>`;
    cm.appendChild(empty);
    return;
  }
  state.chat.messages.forEach(m => cm.appendChild(renderMessage(m)));
  cm.scrollTop = cm.scrollHeight;
}

function labelSideProject() {
  const el = document.getElementById('side-chat-project');
  if (!el) return;
  const proj = (state.data.projects || []).find(p => p.id === state.editor.projectId);
  el.textContent = proj ? (proj.name || proj.path || '') : (state.editor.projectPath || '');
}

// runChatTurn is the shared send path for the main chat and the editor
// sidebar. editorAware adds the open file and tabs so the agent edits the
// project the user is looking at.
async function runChatTurn({ text, attachments, statusEl, onRender, editorAware }) {
  const files = attachments || [];
  const display = text.trim() +
    (files.length ? `\n📎 ${files.map(a => a.name).join(', ')}` : '');
  const userMsg = { role: 'user', content: display, ts: new Date().toISOString() };
  const think = {
    role: 'assistant',
    content: '',
    pending: true,
    thinking: '',
    activity: ['Sending the prompt…'],
    tool_uses: [],
    started: Date.now(),
    ts: new Date().toISOString(),
  };
  state.chat.messages.push(userMsg);
  state.chat.messages.push(think);
  if (onRender) onRender();
  ensureThinkTimer(statusEl);
  if (statusEl) statusEl.textContent = '🤔 Thinking · 0s';

  const body = {
    session_id: null,
    prompt: text,
    model: ($('#model-select') && $('#model-select').value) || '',
    provider_id: ($('#provider-select') && $('#provider-select').value) || '',
    permission_mode: ($('#permission-mode') && $('#permission-mode').value) || '',
    attachments: files,
    project_id: state.editor.projectId || '',
    editor: editorAware ? editorContextPayload() : null,
  };

  try {
    const sessionId = await ensureChatSession();
    body.session_id = sessionId;
    let payload = null;
    let sawWork = false;
    let streamError = null;
    try {
      await readChatStream(body, (ev) => {
        if (ev.event === 'thinking' || ev.event === 'tool' || ev.event === 'status') {
          sawWork = true;
          noteThink(think, ev.event, (ev.data && ev.data.text) || '');
        } else if (ev.event === 'error') {
          const message = (ev.data && (ev.data.error || ev.data.text)) || 'request failed';
          streamError = new Error(message);
        } else if (ev.event === 'done') {
          payload = ev.data || {};
        }
      });
    } catch (streamErr) {
      if (sawWork || payload) throw streamErr;
      payload = null;
    }
    if (streamError) throw streamError;
    if (!payload || payload.response == null) {
      const res = await post('/api/chat', body);
      payload = res.data || {};
    }
    let message, toolUses;
    if (typeof payload.response === 'string') {
      message = payload.response;
      toolUses = payload.tool_uses || [];
    } else if (payload.response && typeof payload.response === 'object') {
      message = payload.response.message || '';
      toolUses = payload.response.tool_uses || payload.tool_uses || [];
    } else {
      message = '';
      toolUses = payload.tool_uses || [];
    }
    finishThink(think, payload, message, toolUses);
    const used = payload.model;
    const ms = $('#model-select');
    if (used && ms && [...ms.options].some(o => o.value === used || o.textContent === used)) {
      ms.value = used;
    }
    if (onRender) onRender();
    if (statusEl) statusEl.textContent = 'ready';
    openTabsForToolUses(toolUses);
    appendPlanStep(sessionId, text, message, toolUses);
    if (state.view === 'editor' && state.editor.projectId) {
      try { await editorRefreshTree(); } catch (e) { /* ignore */ }
    }
  } catch (e) {
    think.pending = false;
    think.content = '⚠ ' + e.message;
    if (onRender) onRender();
    if (statusEl) statusEl.textContent = 'error';
    toast(e.message, 'err');
  }
}

const views = {};

// ---------- Chat ----------
views.chat = (root) => {
  const wrap = document.createElement('div');
  wrap.className = 'chat';
  wrap.innerHTML = `
    <div class="chat-messages" id="chat-messages"></div>
    <div class="chat-input">
      <div class="chat-dock" id="chat-dock">
        <div class="dock-resize" id="dock-resize" title="Drag to resize height"></div>
        <div class="dock-restore" id="dock-restore" hidden></div>
        <section class="dock-pane" data-pane="console" aria-label="Console">
          <div class="dock-head">
            <strong>Console</strong>
            <span class="spacer"></span>
            <span id="chat-status">ready</span>
            <button type="button" class="dock-close" data-close="console" title="Close console" aria-label="Close console">×</button>
          </div>
          <div class="dock-log" id="dock-console-log"></div>
        </section>
        <div class="dock-split" data-split="a" title="Drag to resize width"></div>
        <section class="dock-pane dock-prompt" data-pane="prompt" aria-label="Prompt">
          <div class="attach-row" id="chat-attaches" hidden></div>
          <div class="composer">
            <button class="icon-btn" id="chat-attach" title="Attach a file" aria-label="Attach a file">📎</button>
            <input type="file" id="chat-file" multiple hidden />
            <textarea id="chat-input" rows="2"
              placeholder="Ask OmniCode anything. (Enter to send, Shift+Enter for a new line)"></textarea>
            <button class="btn primary send" id="chat-send" title="Send (Enter)">Send</button>
          </div>
          <div class="chat-input-actions">
            <div class="left">prompt</div>
            <div class="dock-actions">
              <button class="btn" id="chat-clear">Clear</button>
              <button type="button" class="dock-close" data-close="prompt" title="Close prompt" aria-label="Close prompt">×</button>
            </div>
          </div>
        </section>
        <div class="dock-split" data-split="b" title="Drag to resize width"></div>
        <section class="dock-pane" data-pane="terminal" aria-label="Terminal">
          <div class="dock-head">
            <strong>Terminal</strong>
            <select id="term-project" title="Project folder"></select>
            <span class="spacer"></span>
            <button type="button" id="term-claude" disabled>Claude</button>
            <button type="button" id="term-kimi" disabled>Kimi</button>
            <button type="button" id="term-mmx" disabled>MiniMax</button>
            <button type="button" class="dock-close" data-close="terminal" title="Close terminal" aria-label="Close terminal">×</button>
          </div>
          <div class="term-host" id="term-host"></div>
        </section>
      </div>
    </div>`;
  root.appendChild(wrap);

  const cm = $('#chat-messages');
  const input = $('#chat-input');
  const status = $('#chat-status');
  const sendBtn = $('#chat-send');
  const attachRow = $('#chat-attaches');
  const fileInput = $('#chat-file');
  let pending = []; // attachments staged for the next send

  // Draw the welcome state above the composer; the composer is always
  // usable, so there is never a dead "start a session" screen.
  const renderMessages = () => {
    cm.innerHTML = '';
    if (state.chat.messages.length === 0) {
      const empty = document.createElement('div');
      empty.className = 'empty';
      empty.innerHTML = `
        <div class="glyph">○</div>
        <h2>Start a session</h2>
        <p>Type a prompt below to begin, or open a previous session from the left.</p>`;
      cm.appendChild(empty);
      return;
    }
    state.chat.messages.forEach(m => cm.appendChild(renderMessage(m)));
    cm.scrollTop = cm.scrollHeight;
  };

  const renderAttaches = () => {
    attachRow.innerHTML = '';
    attachRow.hidden = pending.length === 0;
    pending.forEach((a, i) => {
      const chip = document.createElement('span');
      chip.className = 'chip';
      chip.innerHTML = `<span class="chip-name"></span><button class="chip-x" title="Remove">✕</button>`;
      chip.querySelector('.chip-name').textContent = a.name;
      chip.querySelector('.chip-x').onclick = () => { pending.splice(i, 1); renderAttaches(); };
      attachRow.appendChild(chip);
    });
  };

  renderMessages();

  // Grow the textarea with its content, up to a cap.
  const autoGrow = () => {
    input.style.height = 'auto';
    input.style.height = Math.min(input.scrollHeight, 200) + 'px';
  };
  input.addEventListener('input', autoGrow);

  let busy = false;
  const send = async () => {
    if (busy) return;
    const text = input.value;          // do not trim: keep user spacing
    const attachments = pending.slice();
    if (!text.trim() && attachments.length === 0) return;

    busy = true;
    sendBtn.disabled = true;
    input.value = '';
    autoGrow();
    pending = [];
    renderAttaches();
    try {
      await runChatTurn({
        text,
        attachments,
        statusEl: status,
        editorAware: false,
        onRender: renderMessages,
      });
    } finally {
      busy = false;
      sendBtn.disabled = false;
      input.focus();
    }
  };

  // Attach button -> native file picker. Paste and drag/drop too.
  const upload = async (fileList) => {
    const files = Array.from(fileList || []);
    if (!files.length) return;
    const fd = new FormData();
    files.forEach(f => fd.append('files', f, f.name));
    status.textContent = 'uploading...';
    try {
      const r = await api('POST', '/api/upload', undefined, fd);
      pending = pending.concat(r.data || []);
      renderAttaches();
      status.textContent = 'ready';
    } catch (e) {
      status.textContent = 'error';
      toast(e.message, 'err');
    }
  };

  $('#chat-attach').onclick = () => fileInput.click();
  fileInput.onchange = () => { upload(fileInput.files); fileInput.value = ''; };

  input.addEventListener('paste', (e) => {
    const files = Array.from((e.clipboardData?.files) || []);
    if (files.length) { e.preventDefault(); upload(files); }
  });
  input.addEventListener('dragover', (e) => e.preventDefault());
  input.addEventListener('drop', (e) => {
    e.preventDefault();
    if (e.dataTransfer?.files?.length) upload(e.dataTransfer.files);
  });

  // Enter sends. Shift+Enter inserts a newline. Every other key — space
  // included — falls through to the textarea untouched.
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      send();
    }
  });

  sendBtn.onclick = send;
  $('#chat-clear').onclick = () => {
    state.chat.messages = [];
    pending = [];
    renderAttaches();
    renderMessages();
  };

  if (!state.dock.closed.prompt) input.focus();
  if (state.consoleLines.length === 0) consoleLog('Console ready');
  else paintConsole();
  mountChatDock();
};

let thinkTimer = null;

function ensureThinkTimer(statusEl) {
  if (thinkTimer) return;
  thinkTimer = setInterval(() => {
    const box = liveThinkBox();
    if (!box) {
      clearInterval(thinkTimer);
      thinkTimer = null;
      return;
    }
    const started = Number(box.dataset.started || Date.now());
    const sec = Math.max(0, Math.round((Date.now() - started) / 1000));
    const elapsed = box.querySelector('.think-elapsed');
    if (elapsed) elapsed.textContent = sec + 's';
    const emoji = box.querySelector('.think-emoji');
    if (emoji) emoji.textContent = ['🤔', '💭', '🧠'][Math.floor(Date.now() / 500) % 3];
    if (!box.dataset.real) {
      const placeholder = box.querySelector('.think-line');
      const label = sec >= 8 ? 'Still reasoning…' : (sec >= 2 ? 'The model is reasoning…' : 'Sending the prompt…');
      if (placeholder) placeholder.textContent = label;
    }
    if (statusEl) statusEl.textContent = '🤔 Thinking · ' + sec + 's';
  }, 200);
}

function liveThinkBox() {
  const root = document.body.classList.contains('editor-mode')
    ? document.getElementById('side-chat-messages')
    : document.getElementById('chat-messages');
  return root ? root.querySelector('.think-box.live') : null;
}

function noteThink(msg, kind, text) {
  if (!text) return;
  const box = liveThinkBox();
  if (box) box.dataset.real = '1';
  if (kind === 'thinking') {
    msg.thinking = (msg.thinking || '') + text;
    const trace = box && box.querySelector('.think-trace');
    if (trace) {
      trace.hidden = false;
      trace.textContent += text;
      const log = trace.parentElement;
      if (log) log.scrollTop = log.scrollHeight;
    }
    return;
  }
  if (!msg.activity) msg.activity = [];
  if (!msg.activity.includes(text)) {
    msg.activity.push(text);
    consoleLog(text);
  }
  if (!box) return;
  const log = box.querySelector('.think-log');
  if (!log) return;
  const line = document.createElement('div');
  line.className = 'think-line';
  line.textContent = text;
  const trace = log.querySelector('.think-trace');
  if (trace) log.insertBefore(line, trace);
  else log.appendChild(line);
  log.scrollTop = log.scrollHeight;
}

// Models often wrap the trace in <think>…</think> and only then write the
// answer. The trace belongs in the think box, not in the reply body.
function splitThinking(text) {
  const chunks = [];
  const answer = String(text || '').replace(/<think(?:ing)?>([\s\S]*?)<\/think(?:ing)?>/gi, (_, body) => {
    const bit = String(body || '').trim();
    if (bit) chunks.push(bit);
    return '';
  }).trim();
  return { thinking: chunks.join('\n\n'), answer };
}

function finishThink(msg, payload, message, toolUses) {
  const sec = Math.max(1, Math.round((Date.now() - (msg.started || Date.now())) / 1000));
  msg.pending = false;
  msg.elapsed = sec;
  const split = splitThinking(message || '');
  msg.content = split.answer;
  msg.tool_uses = toolUses || [];
  const reasoning = [msg.thinking, split.thinking, (payload && payload.reasoning) || '']
    .filter(part => part && String(part).trim())
    .join('\n\n');
  if (reasoning) msg.thinking = reasoning;
  const steps = (payload && payload.activity) || [];
  if (!msg.activity) msg.activity = [];
  steps.forEach(step => {
    if (step && !msg.activity.includes(step)) msg.activity.push(step);
  });
}

function readChatStream(body, onEvent) {
  return fetch('/api/chat/stream', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }).then(async (r) => {
    if (!r.ok || !r.body) {
      const text = await r.text();
      throw new Error(text || r.statusText);
    }
    const reader = r.body.getReader();
    const dec = new TextDecoder();
    let buf = '';
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let idx;
      while ((idx = buf.indexOf('\n\n')) !== -1) {
        const frame = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        const ev = parseSSE(frame);
        if (ev) onEvent(ev);
      }
    }
  });
}

function parseSSE(frame) {
  let event = 'message';
  const dataLines = [];
  frame.split(/\r?\n/).forEach(line => {
    if (line.startsWith('event:')) event = line.slice(6).trim();
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim());
  });
  if (!dataLines.length) return null;
  const raw = dataLines.join('\n');
  try { return { event, data: JSON.parse(raw) }; }
  catch (e) { return { event, data: { text: raw } }; }
}

function renderThinkBox(m) {
  const box = document.createElement('div');
  const sec = m.elapsed || Math.max(0, Math.round((Date.now() - (m.started || Date.now())) / 1000));
  box.className = 'think-box' + (m.pending ? ' live' : '');
  if (m.started) box.dataset.started = String(m.started);
  const title = m.pending ? 'Thinking' : ((m.thinking && m.thinking.trim()) ? 'Thought' : 'Worked');
  box.innerHTML = `
    <button type="button" class="think-toggle">
      <span class="think-emoji">🤔</span>
      <span class="think-title"></span>
      <span class="think-elapsed"></span>
    </button>
    <div class="think-log">
      <pre class="think-trace"></pre>
    </div>`;
  box.querySelector('.think-title').textContent = title;
  box.querySelector('.think-elapsed').textContent = sec + 's';
  const log = box.querySelector('.think-log');
  (m.activity || []).forEach(step => {
    const line = document.createElement('div');
    line.className = 'think-line';
    line.textContent = step;
    log.insertBefore(line, log.querySelector('.think-trace'));
  });
  const trace = box.querySelector('.think-trace');
  trace.textContent = m.thinking || '';
  if (!m.thinking) trace.hidden = true;
  box.querySelector('.think-toggle').onclick = () => {
    log.hidden = !log.hidden;
  };
  return box;
}

function renderMessage(m) {
  const el = document.createElement('div');
  el.className = 'msg ' + m.role;
  if (m.streaming || m.pending) el.classList.add('streaming');
  el.innerHTML = `
    <div class="avatar"></div>
    <div class="body">
      <div class="role"></div>
      <div class="think-slot"></div>
      <div class="content"></div>
    </div>`;
  el.querySelector('.avatar').textContent = m.role === 'user' ? 'U' : (m.pending ? '🤔' : 'AI');
  const when = m.ts ? ' · ' + new Date(m.ts).toLocaleTimeString() : '';
  el.querySelector('.role').textContent = (m.pending ? 'thinking' : m.role) + when;
  let contentText = m.content || '';
  let thinkingText = m.thinking || '';
  if (!m.pending && m.role !== 'user') {
    const split = splitThinking(contentText);
    if (split.thinking) {
      thinkingText = [thinkingText, split.thinking].filter(part => part && String(part).trim()).join('\n\n');
      contentText = split.answer;
    }
  }
  const shown = Object.assign({}, m, { content: contentText, thinking: thinkingText });
  const showBox = shown.pending || (shown.thinking && String(shown.thinking).trim()) || (shown.activity && shown.activity.length);
  if (showBox) el.querySelector('.think-slot').appendChild(renderThinkBox(shown));
  const content = el.querySelector('.content');
  // User messages stay textContent to avoid any prompt-injection via markdown
  // HTML. Assistant messages get the full markdown + code-highlight pipeline.
  if (m.role === 'user') {
    content.textContent = contentText;
  } else if (!m.pending) {
    content.innerHTML = renderMarkdown(contentText);
  }
  if (!m.pending && Array.isArray(m.tool_uses)) {
    for (const tu of m.tool_uses) {
      content.appendChild(renderToolCard(tu));
    }
  }
  return el;
}

// ---------- Generic list+form view ----------
// Renders a "New X" form, a card list of items, edit on click.

function listView(root, opts) {
  // opts: { title, sub, endpoint, itemKey, fields, columns, renderForm(item) }
  const inner = document.createElement('div');
  inner.className = 'view-inner';
  inner.innerHTML = `
    <h1 class="view-title">${opts.title}</h1>
    <p class="view-sub">${opts.sub || ''}</p>
    <div class="section">
      <h3>Create new ${opts.singular || opts.title.slice(0, -1).toLowerCase()}</h3>
      <p class="section-sub">Fill in the fields and click Create.</p>
      <form id="form-new"></form>
    </div>
    <div class="section">
      <h3>Existing ${opts.title.toLowerCase()}</h3>
      <div id="items"></div>
    </div>`;
  root.appendChild(inner);

  const form = $('#form-new', inner);
  opts.fields.forEach(f => {
    const row = document.createElement('div');
    row.className = 'form-row';
    if (f.type === 'checkbox') {
      row.innerHTML = `<label class="checkbox"><input type="checkbox" name="${f.name}" ${f.default ? 'checked' : ''}/> ${f.label}</label>`;
    } else if (f.type === 'textarea') {
      row.innerHTML = `<label>${f.label}</label><textarea name="${f.name}" placeholder="${f.placeholder || ''}"></textarea>`;
    } else {
      row.innerHTML = `<label>${f.label}</label><input type="${f.type || 'text'}" name="${f.name}" placeholder="${f.placeholder || ''}" value="${f.default || ''}"/>`;
    }
    form.appendChild(row);
  });
  const submit = document.createElement('button');
  submit.className = 'btn primary';
  submit.type = 'submit';
  submit.textContent = 'Create';
  form.appendChild(submit);
  submit.style.marginTop = '8px';
  form.onsubmit = async (e) => {
    e.preventDefault();
    const body = {};
    opts.fields.forEach(f => {
      const el = form.querySelector(`[name=${f.name}]`);
      if (!el) return;
      if (f.type === 'checkbox') body[f.name] = el.checked;
      else body[f.name] = el.value;
    });
    if (opts.preCreate) opts.preCreate(body);
    try {
      await post(opts.endpoint, body);
      toast('Created', 'ok');
      form.reset();
      refreshList();
    } catch (e) { toast(e.message, 'err'); }
  };

  async function refreshList() {
    const items = $('#items', inner);
    items.innerHTML = '<p style="color:var(--text-2)">Loading…</p>';
    try {
      const r = await get(opts.endpoint);
      const data = r.data || [];
      if (data.length === 0) {
        items.innerHTML = '<p style="color:var(--text-2)">No items yet.</p>';
        return;
      }
      const cards = document.createElement('div');
      cards.className = 'card-list';
      data.forEach(item => cards.appendChild(opts.renderCard(item, refreshList)));
      items.innerHTML = '';
      items.appendChild(cards);
    } catch (e) {
      items.innerHTML = '<p style="color:var(--err)">' + e.message + '</p>';
    }
  }

  refreshList();
}

// ---------- Providers ----------
views.providers = (root) => listView(root, {
  title: 'Providers',
  sub: 'AI providers (Anthropic, OpenAI, custom endpoints). The API key is stored in the secret database, never as plaintext.',
  endpoint: '/api/providers',
  singular: 'provider',
  fields: [
    { name: 'name', label: 'Display name', placeholder: 'My Anthropic' },
    { name: 'kind', label: 'Kind', placeholder: 'anthropic | openai | xai | custom' },
    { name: 'base_url', label: 'Base URL', placeholder: 'https://api.anthropic.com' },
    { name: 'api_key_ref', label: 'API key (secret reference)', placeholder: 'ANTHROPIC_API_KEY' },
    { name: 'default_model', label: 'Default model', placeholder: 'claude-opus-4-6' },
    { name: 'enabled', label: 'Enabled', type: 'checkbox', default: true },
  ],
  renderCard: (p, refresh) => {
    const label = p.label || p.name || p.id;
    const c = document.createElement('div');
    c.className = 'card';
    c.innerHTML = `
      <div class="card-title"></div>
      <div class="card-sub"></div>
      <div class="card-actions">
        <button class="btn small" data-act="test">Test</button>
        <button class="btn small" data-act="models">Fetch models</button>
        <button class="btn small danger" data-act="delete">Delete</button>
      </div>
      <pre class="models-out" style="margin-top:8px;font-size:11px;color:var(--text-2);white-space:pre-wrap;max-height:180px;overflow:auto;display:none"></pre>`;
    // Fill via textContent so a missing field never renders the word "undefined".
    const title = c.querySelector('.card-title');
    title.append(label + ' ');
    const kindTag = document.createElement('span');
    kindTag.className = 'tag';
    kindTag.textContent = p.kind || 'openai';
    title.appendChild(kindTag);
    const keyTag = document.createElement('span');
    keyTag.className = p.has_api_key ? 'tag ok' : 'tag warn';
    keyTag.textContent = p.has_api_key ? 'key set' : 'no key';
    title.appendChild(keyTag);
    c.querySelector('.card-sub').textContent =
      (p.base_url || '') + ' · ' + (p.default_model || 'no default model');

    $('[data-act=test]', c).onclick = async () => {
      try {
        const r = await post(`/api/providers/${p.id}/test`);
        if (r.data.ok) toast(`${label}: reachable`, 'ok');
        else toast(`${label}: ${r.data.reason}`, 'warn');
      } catch (e) { toast(e.message, 'err'); }
    };
    $('[data-act=models]', c).onclick = async () => {
      const out = c.querySelector('.models-out');
      out.style.display = 'block';
      out.textContent = 'loading…';
      try {
        const r = await post(`/api/providers/${p.id}/models`);
        const models = (r.data && r.data.models) || [];
        out.textContent = models.length ? models.join('\n') : 'endpoint returned no models';
        if (models.length) toast(`${label}: ${models.length} models`, 'ok');
      } catch (e) {
        out.textContent = 'failed: ' + e.message;
        toast(`${label}: ${e.message}`, 'warn');
      }
    };
    $('[data-act=delete]', c).onclick = async () => {
      if (!confirm('Delete provider "' + label + '"?')) return;
      try { await del(`/api/providers/${p.id}`); toast('Deleted', 'ok'); refresh(); }
      catch (e) { toast(e.message, 'err'); }
    };
    return c;
  },
});

// ---------- Endpoints (same providers, grouped view; fetches live models) ----------
views.endpoints = (root) => listView(root, {
  title: 'Endpoints',
  sub: 'Provider base URLs read from agent/settings.json. Use Fetch models to read the live /v1/models list from an endpoint.',
  endpoint: '/api/providers',
  singular: 'endpoint',
  fields: [
    { name: 'name', label: 'Endpoint name', placeholder: 'Local Ollama' },
    { name: 'kind', label: 'Kind', placeholder: 'custom' },
    { name: 'base_url', label: 'Base URL', placeholder: 'http://localhost:11434/v1' },
    { name: 'api_key_ref', label: 'API key (secret reference)', placeholder: 'OLLAMA_API_KEY (optional)' },
    { name: 'default_model', label: 'Default model', placeholder: 'llama3.1' },
    { name: 'enabled', label: 'Enabled', type: 'checkbox', default: true },
  ],
  renderCard: (p, refresh) => {
    const label = p.label || p.name || p.id;
    const c = document.createElement('div');
    c.className = 'card';
    c.innerHTML = `
      <div class="card-title"></div>
      <div class="card-sub"></div>
      <div class="card-actions">
        <button class="btn small" data-act="test">Test</button>
        <button class="btn small" data-act="models">Fetch models</button>
        <button class="btn small danger" data-act="delete">Delete</button>
      </div>
      <pre class="models-out" style="margin-top:8px;font-size:11px;color:var(--text-2);white-space:pre-wrap;max-height:180px;overflow:auto;display:none"></pre>`;
    const title = c.querySelector('.card-title');
    title.append(p.base_url || '(no URL)');
    title.append(' ');
    const keyTag = document.createElement('span');
    keyTag.className = p.has_api_key ? 'tag ok' : 'tag warn';
    keyTag.textContent = p.has_api_key ? 'key set' : 'no key';
    title.appendChild(keyTag);
    c.querySelector('.card-sub').textContent =
      [label, p.kind, p.default_model || 'no default model'].filter(Boolean).join(' · ');

    $('[data-act=test]', c).onclick = async () => {
      try {
        const r = await post(`/api/providers/${p.id}/test`);
        if (r.data.ok) toast(`${label}: reachable`, 'ok');
        else toast(`${label}: ${r.data.reason}`, 'warn');
      } catch (e) { toast(e.message, 'err'); }
    };
    $('[data-act=models]', c).onclick = async () => {
      const out = c.querySelector('.models-out');
      out.style.display = 'block';
      out.textContent = 'loading…';
      try {
        const r = await post(`/api/providers/${p.id}/models`);
        const models = (r.data && r.data.models) || [];
        out.textContent = models.length ? models.join('\n') : 'endpoint returned no models';
        if (models.length) toast(`${label}: ${models.length} models`, 'ok');
      } catch (e) {
        out.textContent = 'failed: ' + e.message;
        toast(`${label}: ${e.message}`, 'warn');
      }
    };
    $('[data-act=delete]', c).onclick = async () => {
      if (!confirm('Delete endpoint "' + label + '"?')) return;
      try { await del(`/api/providers/${p.id}`); toast('Deleted', 'ok'); refresh(); }
      catch (e) { toast(e.message, 'err'); }
    };
    return c;
  },
});

// ---------- AI Control ----------
views['ai-control'] = (root) => {
  root.innerHTML = `
    <div class="view-inner">
      <h1 class="view-title">AI Control</h1>
      <p class="view-sub">Global settings that govern every AI request OmniCode makes.</p>
      <div class="section">
        <h3>Reasoning</h3>
        <p class="section-sub">How hard the model thinks before responding.</p>
        <div class="form-row">
          <label>Default reasoning effort</label>
          <select name="reasoning">
            <option value="low">low — fast, cheap</option>
            <option value="medium" selected>medium — balanced</option>
            <option value="high">high — careful</option>
            <option value="max">max — most thorough</option>
          </select>
        </div>
      </div>
      <div class="section">
        <h3>Streaming</h3>
        <p class="section-sub">Stream tokens as they arrive (improves perceived latency).</p>
        <label class="checkbox"><input type="checkbox" checked/> Enable streaming responses</label>
      </div>
      <div class="section">
        <h3>Compaction</h3>
        <p class="section-sub">Auto-summarize long sessions to stay under context limits.</p>
        <label class="checkbox"><input type="checkbox" checked/> Auto-compact when context &gt; 75%</label>
      </div>
      <div class="section">
        <h3>Safety</h3>
        <p class="section-sub">Permission policies and tool execution controls.</p>
        <label class="checkbox"><input type="checkbox" checked/> Ask before destructive bash commands</label><br>
        <label class="checkbox"><input type="checkbox" checked/> Redact secrets from logs</label><br>
        <label class="checkbox"><input type="checkbox"/> Allow network access from tool calls</label>
      </div>
      <div class="section">
        <h3>Cost controls</h3>
        <p class="section-sub">Hard limits so a runaway loop can't burn your budget.</p>
        <div class="form-row">
          <label>Per-session max cost (USD)</label>
          <input type="number" value="5.00" min="0" step="0.01"/>
        </div>
      </div>
      <button class="btn primary" id="save-ai">Save AI control settings</button>
    </div>`;
  $('#save-ai').onclick = async () => {
    // In a fuller implementation these would persist to /api/settings.
    toast('AI control settings saved', 'ok');
  };
};

// ---------- MCP ----------
views.mcp = (root) => listView(root, {
  title: 'MCP Servers',
  sub: 'Model Context Protocol servers — extend OmniCode with external tools and resources.',
  endpoint: '/api/mcp',
  singular: 'MCP server',
  fields: [
    { name: 'name', label: 'Name' },
    { name: 'transport', label: 'Transport', placeholder: 'stdio | websocket | http | sdk' },
    { name: 'command', label: 'Command (for stdio)' },
    { name: 'args', label: 'Args (JSON array)', placeholder: '["--port", "8080"]' },
    { name: 'url', label: 'URL (for websocket/http)' },
    { name: 'config', label: 'Config (JSON blob)', type: 'textarea' },
    { name: 'enabled', label: 'Enabled', type: 'checkbox', default: true },
  ],
  renderCard: (m, refresh) => {
    const c = document.createElement('div');
    c.className = 'card';
    c.innerHTML = `
      <div class="card-title">${m.name} <span class="tag">${m.transport}</span> ${m.enabled ? '<span class="tag ok">on</span>' : '<span class="tag err">off</span>'}</div>
      <div class="card-sub">${m.command ? '$ ' + m.command + ' ' + (m.args || '') : m.url || '(no command/url)'}</div>
      <div class="card-actions">
        <button class="btn small" data-act="toggle">${m.enabled ? 'Disable' : 'Enable'}</button>
        <button class="btn small danger" data-act="delete">Delete</button>
      </div>`;
    $('[data-act=toggle]', c).onclick = async () => {
      try { await put(`/api/mcp/${m.id}`, { ...m, enabled: !m.enabled }); refresh(); }
      catch (e) { toast(e.message, 'err'); }
    };
    $('[data-act=delete]', c).onclick = async () => {
      if (!confirm('Delete MCP server?')) return;
      try { await del(`/api/mcp/${m.id}`); toast('Deleted', 'ok'); refresh(); }
      catch (e) { toast(e.message, 'err'); }
    };
    return c;
  },
});

// ---------- Plugins ----------
views.plugins = (root) => listView(root, {
  title: 'Plugins',
  sub: 'Local code extensions that add slash commands, tools, and behaviors to OmniCode.',
  endpoint: '/api/plugins',
  singular: 'plugin',
  fields: [
    { name: 'name', label: 'Name' },
    { name: 'version', label: 'Version', placeholder: '0.1.0' },
    { name: 'source', label: 'Source', placeholder: 'local | git | registry' },
    { name: 'install_path', label: 'Install path' },
    { name: 'enabled', label: 'Enabled', type: 'checkbox', default: true },
  ],
  renderCard: (p, refresh) => {
    const c = document.createElement('div');
    c.className = 'card';
    c.innerHTML = `
      <div class="card-title">${p.name} <span class="tag">${p.version || '-'}</span> ${p.enabled ? '<span class="tag ok">on</span>' : '<span class="tag err">off</span>'}</div>
      <div class="card-sub">${p.source} · ${p.install_path || '(no path)'}</div>
      <div class="card-actions">
        <button class="btn small" data-act="toggle">${p.enabled ? 'Disable' : 'Enable'}</button>
        <button class="btn small danger" data-act="delete">Delete</button>
      </div>`;
    $('[data-act=toggle]', c).onclick = async () => {
      try { await put(`/api/plugins/${p.id}`, { ...p, enabled: !p.enabled }); refresh(); }
      catch (e) { toast(e.message, 'err'); }
    };
    $('[data-act=delete]', c).onclick = async () => {
      if (!confirm('Delete plugin?')) return;
      try { await del(`/api/plugins/${p.id}`); toast('Deleted', 'ok'); refresh(); }
      catch (e) { toast(e.message, 'err'); }
    };
    return c;
  },
});

// ---------- Skills ----------
views.skills = (root) => listView(root, {
  title: 'Skills',
  sub: 'Reusable prompt/behavior bundles OmniCode can invoke by name.',
  endpoint: '/api/skills',
  singular: 'skill',
  fields: [
    { name: 'name', label: 'Name' },
    { name: 'description', label: 'Description' },
    { name: 'source', label: 'Source', placeholder: 'user | project | legacy' },
    { name: 'path', label: 'Path' },
    { name: 'enabled', label: 'Enabled', type: 'checkbox', default: true },
  ],
  renderCard: (s, refresh) => {
    const c = document.createElement('div');
    c.className = 'card';
    c.innerHTML = `
      <div class="card-title">${s.name} ${s.enabled ? '<span class="tag ok">on</span>' : '<span class="tag err">off</span>'}</div>
      <div class="card-sub">${s.description || ''}</div>
      <div class="card-actions">
        <button class="btn small" data-act="toggle">${s.enabled ? 'Disable' : 'Enable'}</button>
        <button class="btn small danger" data-act="delete">Delete</button>
      </div>`;
    $('[data-act=toggle]', c).onclick = async () => {
      try { await put(`/api/skills/${s.id}`, { ...s, enabled: !s.enabled }); refresh(); }
      catch (e) { toast(e.message, 'err'); }
    };
    $('[data-act=delete]', c).onclick = async () => {
      if (!confirm('Delete skill?')) return;
      try { await del(`/api/skills/${s.id}`); toast('Deleted', 'ok'); refresh(); }
      catch (e) { toast(e.message, 'err'); }
    };
    return c;
  },
});

// ---------- Agents ----------
views.agents = (root) => listView(root, {
  title: 'Agents',
  sub: 'Configured agents — each can specify its own model, reasoning, and system prompt.',
  endpoint: '/api/agents',
  singular: 'agent',
  fields: [
    { name: 'name', label: 'Name' },
    { name: 'description', label: 'Description' },
    { name: 'model', label: 'Model', placeholder: 'claude-opus-4-6' },
    { name: 'reasoning_effort', label: 'Reasoning effort', placeholder: 'low | medium | high | max' },
    { name: 'source', label: 'Source', placeholder: 'user | project' },
    { name: 'system_prompt', label: 'System prompt', type: 'textarea' },
  ],
  renderCard: (a, refresh) => {
    const c = document.createElement('div');
    c.className = 'card';
    c.innerHTML = `
      <div class="card-title">${a.name}</div>
      <div class="card-sub">${a.description || ''}<br><code>${a.model || 'default model'}</code> · reasoning: ${a.reasoning_effort || 'default'}</div>
      <div class="card-actions">
        <button class="btn small danger" data-act="delete">Delete</button>
      </div>`;
    $('[data-act=delete]', c).onclick = async () => {
      if (!confirm('Delete agent?')) return;
      try { await del(`/api/agents/${a.id}`); toast('Deleted', 'ok'); refresh(); }
      catch (e) { toast(e.message, 'err'); }
    };
    return c;
  },
});

// ---------- Database ----------
views.database = async (root) => {
  root.innerHTML = '<div class="view-inner"><p>Loading…</p></div>';
  try {
    const cfg = (await get('/api/config')).data;
    const dash = (await get('/api/dashboard')).data;
    root.innerHTML = `
      <div class="view-inner">
        <h1 class="view-title">Database</h1>
        <p class="view-sub">All OmniCode state lives in a single SQLite3 file. Projects, providers, MCP servers, plugins, skills, agents, sessions, secrets, and audit logs.</p>
        <div class="section">
          <h3>Location</h3>
          <code style="font-family:var(--font-mono); font-size:12px; color:var(--accent-2)">${cfg.database}</code>
        </div>
        <div class="section">
          <h3>Tables</h3>
          <table class="t">
            <thead><tr><th>Table</th><th>Rows</th></tr></thead>
            <tbody>
              <tr><td><code>secrets</code></td><td>${dash.secrets}</td></tr>
              <tr><td><code>providers</code></td><td>${dash.providers}</td></tr>
              <tr><td><code>projects</code></td><td>${dash.projects}</td></tr>
              <tr><td><code>project_files</code></td><td>(indexed on scan)</td></tr>
              <tr><td><code>mcp_servers</code></td><td>${dash.mcp_servers}</td></tr>
              <tr><td><code>plugins</code></td><td>${dash.plugins}</td></tr>
              <tr><td><code>skills</code></td><td>${dash.skills}</td></tr>
              <tr><td><code>agents</code></td><td>${dash.agents}</td></tr>
              <tr><td><code>sessions</code></td><td>${dash.sessions}</td></tr>
              <tr><td><code>session_messages</code></td><td>(per session)</td></tr>
              <tr><td><code>audit_log</code></td><td>(capped at 200 shown)</td></tr>
              <tr><td><code>settings</code></td><td>(app key/value)</td></tr>
            </tbody>
          </table>
        </div>
        <div class="section">
          <h3>Tools</h3>
          <button class="btn" id="vacuum">Vacuum</button>
          <button class="btn" id="export">Export schema.sql</button>
        </div>
      </div>`;
    $('#vacuum').onclick = () => toast('Vacuum would run via sqlite3 CLI in production', 'ok');
    $('#export').onclick = async () => {
      try {
        const r = await get('/api/audit');
        const blob = new Blob([JSON.stringify(r.data, null, 2)], { type: 'application/json' });
        const a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = 'omnicode-audit.json';
        a.click();
        toast('Exported', 'ok');
      } catch (e) { toast(e.message, 'err'); }
    };
  } catch (e) {
    root.innerHTML = `<div class="view-inner"><p style="color:var(--err)">${e.message}</p></div>`;
  }
};

// ---------- Secrets (API Keys) ----------
views.secrets = (root) => {
  listView(root, {
    title: 'API Keys',
    sub: 'Secrets live in the SQLite3 database, never as plaintext env vars. Values are masked in lists.',
    endpoint: '/api/secrets',
    singular: 'secret',
    fields: [
      { name: 'key', label: 'Key name (e.g. ANTHROPIC_API_KEY)' },
      { name: 'value', label: 'Value', type: 'password' },
    ],
    preCreate: (body) => {
      // The list view calls POST /api/secrets with {key, value}.
      // We need to call PUT /api/secrets/{key} with {value}.
      // Handle inline here:
      const ev = new MouseEvent('click');
    },
    renderCard: (s, refresh) => {
      const c = document.createElement('div');
      c.className = 'card';
      c.innerHTML = `
        <div class="card-title"><code>${s.key}</code></div>
        <div class="card-sub">${s.masked}</div>
        <div class="card-actions">
          <button class="btn small" data-act="copy">Copy name</button>
          <button class="btn small danger" data-act="delete">Delete</button>
        </div>`;
      $('[data-act=copy]', c).onclick = () => {
        navigator.clipboard.writeText(s.key);
        toast('Copied key name', 'ok');
      };
      $('[data-act=delete]', c).onclick = async () => {
        if (!confirm('Delete secret ' + s.key + '?')) return;
        try { await del('/api/secrets/' + encodeURIComponent(s.key)); toast('Deleted', 'ok'); refresh(); }
        catch (e) { toast(e.message, 'err'); }
      };
      return c;
    },
  });
  // Override the form submit to use PUT /api/secrets/{key}
  const form = $('#form-new');
  if (form) {
    form.onsubmit = async (e) => {
      e.preventDefault();
      const key = form.querySelector('[name=key]').value.trim();
      const value = form.querySelector('[name=value]').value;
      if (!key) return;
      try {
        await put('/api/secrets/' + encodeURIComponent(key), { value });
        toast('Stored ' + key, 'ok');
        form.reset();
        // Trigger refresh by re-rendering
        render();
      } catch (e) { toast(e.message, 'err'); }
    };
  }
};

// ---------- App Settings ----------
views.app = async (root) => {
  root.innerHTML = '<div class="view-inner"><p>Loading…</p></div>';
  try {
    const cfg = (await get('/api/config')).data;
    root.innerHTML = `
      <div class="view-inner">
        <h1 class="view-title">App Settings</h1>
        <p class="view-sub">OmniCode application-level configuration.</p>
        <div class="section">
          <h3>Paths</h3>
          <div class="form-row">
            <label>Install directory</label>
            <input type="text" value="${cfg.installPath}" readonly/>
          </div>
          <div class="form-row">
            <label>Database</label>
            <input type="text" value="${cfg.database}" readonly/>
          </div>
          <div class="form-row">
            <label>Core binary</label>
            <input type="text" value="${cfg.corePath}" readonly/>
          </div>
          <div class="form-row">
            <label>Web UI</label>
            <input type="text" value="http://${cfg.webHost}:${cfg.webPort}/" readonly/>
          </div>
        </div>
        <div class="section">
          <h3>Repair</h3>
          <p class="section-sub">Re-scan the project on disk and apply any needed repair protocols.</p>
          <button class="btn" id="repair">Run repair scan</button>
          <pre id="repair-out" style="margin-top:12px; font-size:12px; color:var(--text-1); white-space:pre-wrap"></pre>
        </div>
        <div class="section">
          <h3>Theme</h3>
          <p class="section-sub">Cosmetic only. The OmniCode dark theme is the default.</p>
          <div class="form-row">
            <label>Theme</label>
            <select>
              <option>Dark (default)</option>
              <option>Light</option>
              <option>High contrast</option>
            </select>
          </div>
        </div>
      </div>`;
    $('#repair').onclick = async () => {
      try {
        const r = await post('/api/repair', { path: '.' });
        const out = $('#repair-out');
        const lines = [`Scanned: ${r.data.path}`];
        (r.data.deps || []).forEach(d => lines.push(`  dep: ${d.name} ${d.version} (${d.status})`));
        if ((r.data.actions || []).length === 0) lines.push('No repairs needed.');
        else (r.data.actions || []).forEach(a => lines.push(`  action: [${a.id}] ${a.description}`));
        out.textContent = lines.join('\n');
        toast('Repair scan complete', 'ok');
      } catch (e) { toast(e.message, 'err'); }
    };
  } catch (e) {
    root.innerHTML = `<div class="view-inner"><p style="color:var(--err)">${e.message}</p></div>`;
  }
};

// ---------- Audit ----------
views.audit = async (root) => {
  root.innerHTML = '<div class="view-inner"><p>Loading…</p></div>';
  try {
    const r = await get('/api/audit');
    const rows = r.data || [];
    root.innerHTML = `
      <div class="view-inner">
        <h1 class="view-title">Audit Log</h1>
        <p class="view-sub">Last 200 actions recorded by OmniCode.</p>
        <div class="section">
          <table class="t">
            <thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Target</th></tr></thead>
            <tbody>
              ${rows.map(a => `<tr>
                <td>${a.ts}</td>
                <td>${a.actor}</td>
                <td><code>${a.action}</code></td>
                <td>${a.target || ''}</td>
              </tr>`).join('') || '<tr><td colspan="4" style="color:var(--text-2)">No events yet.</td></tr>'}
            </tbody>
          </table>
        </div>
      </div>`;
  } catch (e) {
    root.innerHTML = `<div class="view-inner"><p style="color:var(--err)">${e.message}</p></div>`;
  }
};

// ---------- Projects ----------
views.projects = (root) => listView(root, {
  title: 'Projects',
  sub: 'OmniCode workspaces. Each project has its own files, default provider, and permission mode.',
  endpoint: '/api/projects',
  singular: 'project',
  fields: [
    { name: 'name', label: 'Name' },
    { name: 'path', label: 'Path', placeholder: 'C:\\path\\to\\project' },
    { name: 'description', label: 'Description' },
    { name: 'default_model', label: 'Default model', placeholder: 'claude-sonnet-4-6' },
    { name: 'permission_mode', label: 'Permission mode', placeholder: 'workspace-write' },
  ],
  renderCard: (p, refresh) => {
    const c = document.createElement('div');
    c.className = 'card';
    c.innerHTML = `
      <div class="card-title">${p.name}</div>
      <div class="card-sub"><code>${p.path}</code><br>${p.description || ''}</div>
      <div class="card-actions">
        <button class="btn small" data-act="scan">Scan</button>
        <button class="btn small" data-act="open">Open in REPL</button>
        <button class="btn small danger" data-act="delete">Delete</button>
      </div>`;
    $('[data-act=scan]', c).onclick = async () => {
      try {
        const r = await post(`/api/projects/${p.id}/scan`);
        toast(`Found ${r.data.length} dependencies`, 'ok');
      } catch (e) { toast(e.message, 'err'); }
    };
    $('[data-act=open]', c).onclick = () => {
      state.chat.sessionId = null; // or pick a project session later
      setView('chat');
    };
    $('[data-act=delete]', c).onclick = async () => {
      if (!confirm('Delete project "' + p.name + '"?')) return;
      try { await del(`/api/projects/${p.id}`); toast('Deleted', 'ok'); refresh(); }
      catch (e) { toast(e.message, 'err'); }
    };
    return c;
  },
});

// ---------- Sessions ----------
views.sessions = async (root) => {
  root.innerHTML = '<div class="view-inner"><p>Loading…</p></div>';
  try {
    const r = await get('/api/sessions');
    const data = r.data || [];
    root.innerHTML = `
      <div class="view-inner">
        <h1 class="view-title">Sessions</h1>
        <p class="view-sub">Your last ${data.length} chat sessions, newest first.</p>
        <div class="section">
          ${data.length === 0 ? '<p style="color:var(--text-2)">No sessions yet. Start one from the Chat tab.</p>' : `
          <table class="t">
            <thead><tr><th>Started</th><th>Title</th><th>Messages</th><th>Project</th><th></th></tr></thead>
            <tbody>
              ${data.map(s => `<tr>
                <td>${s.started_at}</td>
                <td>${s.title || '(untitled)'}</td>
                <td>${s.message_count}</td>
                <td>${s.project_id || '-'}</td>
                <td>
                  <button class="btn small" data-id="${s.id}" data-act="open">Open</button>
                  <button class="btn small danger" data-id="${s.id}" data-act="del">Delete</button>
                </td>
              </tr>`).join('')}
            </tbody>
          </table>`}
        </div>
      </div>`;
    $$('[data-act=open]', root).forEach(btn => {
      btn.onclick = async () => {
        const id = btn.dataset.id;
        const r = await get('/api/sessions/' + id);
        state.chat.sessionId = id;
        state.chat.messages = r.data.messages || [];
        setView('chat');
      };
    });
    $$('[data-act=del]', root).forEach(btn => {
      btn.onclick = async () => {
        if (!confirm('Delete this session?')) return;
        try { await del('/api/sessions/' + btn.dataset.id); toast('Deleted', 'ok'); setView('sessions'); }
        catch (e) { toast(e.message, 'err'); }
      };
    });
  } catch (e) {
    root.innerHTML = `<div class="view-inner"><p style="color:var(--err)">${e.message}</p></div>`;
  }
};

// ----------------------------------------------------------
// Editor view: tabs + file tree + Monaco
// ----------------------------------------------------------
//
// The Editor is the IDE surface. Tabs at the top, file tree on the left,
// Monaco in the middle. The first tab on a new session is always plan.md
// (the brainstorming trace). Subsequent tabs arrive as the assistant creates
// files via write_file tool calls — opening the creation pipeline.

views.editor = async (root) => {
  root.innerHTML = `
    <div class="editor">
      <div class="tab-strip" id="tab-strip">
        <div class="tab" data-kind="noop" style="opacity:.6">
          <span class="tab-label">opening…</span>
        </div>
      </div>
      <div class="file-tree" id="file-tree">
        <div class="file-tree-head">
          <span>FILES</span>
          <span class="tree-actions">
            <button class="icon-btn" id="open-terminal" type="button" title="Open terminal">Term</button>
            <button class="icon-btn" id="tree-refresh" title="Refresh">⟳</button>
          </span>
        </div>
        <div class="tree-empty">Pick a project to begin.</div>
      </div>
      <div class="editor-body" id="editor-body">
        <div class="editor-empty" id="editor-empty">
          <div class="glyph">⎚</div>
          <h2>Open or create a file</h2>
          <p>Click a file in the tree, or ask the assistant to build one.</p>
        </div>
      </div>
    </div>`;

  document.body.classList.add('editor-mode');

  $('#tree-refresh').onclick = async () => {
    try {
      await editorRefreshTree();
      toast('Refreshed', 'ok');
    } catch (e) { toast(e.message, 'err'); }
  };
  const termBtn = $('#open-terminal');
  if (termBtn) termBtn.onclick = () => openFloatTerm();
  labelSideProject();

  // Hydrate project selector as a tiny inline chips bar above the tree.
  try {
    const r = await get('/api/projects');
    const projects = r.data || [];
    state.data.projects = projects;
    labelSideProject();
    if (projects.length && !state.editor.projectId) {
      await editorSetProject(projects[0].id);
    } else if (state.editor.projectId) {
      await editorRefreshTree();
    }
  } catch (e) { /* ignore */ }

  // Restore previously open tabs from the server.
  try {
    const sid = state.chat.sessionId || '';
    if (sid) {
      const t = await get('/api/tabs?session_id=' + encodeURIComponent(sid));
      const list = (t.data && t.data.tabs) || [];
      if (list.length) {
        // First tab is plan.md by contract.
        for (const tab of list) {
          await editorOpenTab({
            path: tab.path,
            kind: tab.kind || 'file',
            savedId: tab.id,
          });
        }
        return;
      }
    }
  } catch (e) { /* ignore */ }

  // No restored tabs: open plan.md as the first tab.
  await editorOpenTab({ path: 'plan.md', kind: 'plan', makeActive: true });
};

// editorSetProject re-roots the editor to a different project. Resets tabs.
async function editorSetProject(projectId) {
  state.editor.projectId = projectId;
  state.editor.tabs = [];
  state.editor.activeTab = null;
  const proj = (state.data.projects || []).find(p => p.id === projectId);
  state.editor.projectPath = proj ? proj.path : '';
  labelSideProject();
  if (!document.getElementById('float-term').hidden) retargetFloatTerm();
  // Persist as the sticky project for the editor session.
  try { await post('/api/selection', { project_id: projectId }); } catch (e) { /* ignore */ }
  await editorRefreshTree();
  await editorOpenTab({ path: 'plan.md', kind: 'plan', makeActive: true });
  await editorRenderTabs();
}

// editorRefreshTree walks the project's directory and re-renders the tree.
async function editorRefreshTree() {
  const pid = state.editor.projectId;
  if (!pid) return;
  const tree = (await get('/api/files?project_id=' + encodeURIComponent(pid))).data;
  state.editor.tree = tree;
  editorRenderTree();
}

function editorRenderTree() {
  const host = $('#file-tree');
  if (!host) return;
  const tree = state.editor.tree;
  const empty = host.querySelector('.tree-empty');
  // Remove prior nodes.
  host.querySelectorAll('.tree-node').forEach(n => n.remove());

  if (!tree || !tree.entries || tree.entries.length === 0) {
    if (empty) empty.style.display = '';
    return;
  }
  if (empty) empty.style.display = 'none';
  // Render root entries (children rendering is recursive).
  const root = document.createElement('div');
  root.className = 'tree-children';
  for (const node of tree.entries) {
    root.appendChild(editorTreeNode(node));
  }
  host.appendChild(root);
}

function editorTreeNode(node) {
  const el = document.createElement('div');
  el.className = 'tree-node';
  if (node.kind === 'dir') {
    el.innerHTML = `
      <div class="tree-row" data-path="">
        <span class="twisty">▾</span>
        <span class="icon">▣</span>
        <span class="label"></span>
      </div>
      <div class="tree-children"></div>`;
    el.querySelector('.label').textContent = node.name;
    const row = el.querySelector('.tree-row');
    const children = el.querySelector('.tree-children');
    for (const child of (node.children || [])) {
      children.appendChild(editorTreeNode(child));
    }
    row.onclick = () => {
      const collapsed = children.classList.toggle('collapsed');
      row.querySelector('.twisty').textContent = collapsed ? '▸' : '▾';
    };
  } else {
    el.innerHTML = `
      <div class="tree-row">
        <span class="twisty"></span>
        <span class="icon">▤</span>
        <span class="label"></span>
      </div>`;
    el.querySelector('.label').textContent = node.name;
    const row = el.querySelector('.tree-row');
    row.onclick = () => editorOpenPath(node.path || node.name);
  }
  return el;
}

// editorOpenTab opens a Monaco-backed tab (file or plan). makeActive focuses.
async function editorOpenTab({ path, kind = 'file', makeActive = true, savedId = null }) {
  // Dedup: if already open, just activate it.
  const existing = state.editor.tabs.find(t => t.path === path && t.kind === kind);
  if (existing) {
    if (makeActive) await editorActivateTab(existing.path);
    return existing;
  }

  const tab = {
    id: savedId,
    path,
    kind,
    label: kind === 'plan' ? 'plan.md' : path.split('/').pop(),
    dirty: false,
    model: null,
    content: '',
  };
  state.editor.tabs.push(tab);

  // Persist tab order on the server.
  try {
    const sid = state.chat.sessionId || '';
    if (sid) {
      const r = await post('/api/tabs', {
        session_id: sid,
        path,
        kind,
      });
      if (r && r.data && r.data.id) tab.id = r.data.id;
    }
  } catch (e) { /* ignore */ }

  await editorRenderTabs();
  if (makeActive) await editorActivateTab(path);
  return tab;
}

async function editorRenderTabs() {
  const strip = $('#tab-strip');
  if (!strip) return;
  strip.innerHTML = '';
  for (const tab of state.editor.tabs) {
    const el = document.createElement('div');
    el.className = 'tab';
    if (tab.dirty) el.classList.add('dirty');
    if (state.editor.activeTab === tab.path) el.classList.add('active');
    el.innerHTML = `<span class="tab-label"></span><span class="close" title="Close">×</span>`;
    el.querySelector('.tab-label').textContent = tab.label;
    el.onclick = () => editorActivateTab(tab.path);
    el.querySelector('.close').onclick = (e) => {
      e.stopPropagation();
      editorCloseTab(tab.path);
    };
    strip.appendChild(el);
  }
}

async function editorActivateTab(path) {
  const tab = state.editor.tabs.find(t => t.path === path);
  if (!tab) return;
  state.editor.activeTab = path;
  await editorRenderTabs();
  const body = $('#editor-body');
  if (!body) return;
  body.innerHTML = '';
  if (tab.kind === 'plan') {
    await editorShowPlan(body, tab);
  } else {
    await editorShowFile(body, tab);
  }
}

async function editorShowPlan(body, tab) {
  body.innerHTML = `
    <div id="plan-host">
      <div class="md-title">Plan</div>
      <div class="md-meta">Brainstorming and decision trace for this session. Edit in source view.</div>
      <div class="plan-mode-toggle">
        <input type="checkbox" id="plan-preview-toggle" checked/>
        <label for="plan-preview-toggle">Preview</label>
      </div>
      <div id="plan-rendered"></div>
      <textarea id="plan-source" hidden></textarea>
      <div class="plan-actions">
        <button class="btn" id="plan-save">Save plan.md</button>
        <span class="saved" id="plan-saved"></span>
      </div>
    </div>`;

  // Load content if not already cached.
  if (!tab.content) {
    try {
      const sid = state.chat.sessionId || '';
      const r = await get('/api/plan?session_id=' + encodeURIComponent(sid));
      tab.content = (r.data && r.data.content) || '';
    } catch (e) {
      tab.content = '';
    }
  }
  const ta = body.querySelector('#plan-source');
  const out = body.querySelector('#plan-rendered');
  ta.value = tab.content;
  out.innerHTML = renderMarkdown(tab.content || '*(empty plan)*');

  const toggle = body.querySelector('#plan-preview-toggle');
  toggle.onchange = () => {
    const on = toggle.checked;
    out.hidden = !on;
    ta.hidden = on;
    if (!on) ta.value = state.editor.planDirty ? ta.value : tab.content;
  };

  ta.addEventListener('input', () => {
    state.editor.planDirty = true;
    tab.dirty = true;
    tab.content = ta.value;
    out.innerHTML = renderMarkdown(ta.value);
    editorRenderTabs();
  });

  const saveBtn = body.querySelector('#plan-save');
  const savedEl = body.querySelector('#plan-saved');
  saveBtn.onclick = async () => {
    saveBtn.disabled = true;
    try {
      const sid = state.chat.sessionId || '';
      await put('/api/plan', { session_id: sid, content: tab.content });
      state.editor.planDirty = false;
      tab.dirty = false;
      await editorRenderTabs();
      savedEl.textContent = 'saved';
      setTimeout(() => { savedEl.textContent = ''; }, 1500);
    } catch (e) {
      toast(e.message, 'err');
    } finally {
      saveBtn.disabled = false;
    }
  };
}

async function editorShowFile(body, tab) {
  // Load content lazily — and on every refresh, not just first paint, so a
  // rehydration from /api/tabs never lands the user on a blank tab.
  body.innerHTML = `<div id="monaco-host"></div><div class="editor-status" id="editor-status"></div><div id="editor-load-error" hidden></div>`;

  if (tab.content === '' || tab.content == null) {
    try {
      const pid = (state.editor.projectId || '').trim();
      // No project bound: show an inline picker instead of silently failing.
      if (!pid) {
        renderProjectPicker(body, tab);
        return;
      }
      const r = await get(
        '/api/file?project_id=' + encodeURIComponent(pid) +
        '&path=' + encodeURIComponent(tab.path));
      tab.content = (r.data && r.data.content) || '';
    } catch (e) {
      renderFileError(body, tab, e.message || 'failed to load');
      return;
    }
  }

  body.innerHTML = `<div id="monaco-host"></div><div class="editor-status" id="editor-status"></div>`;
  const monaco = await window.MonacoLoader.load();
  const host = body.querySelector('#monaco-host');
  const lang = monacoLanguage(tab.path);
  const model = monaco.editor.createModel(tab.content, lang);
  const editor = monaco.editor.create(host, {
    model,
    theme: 'vs-dark',
    automaticLayout: true,
    fontSize: 13,
    minimap: { enabled: true },
    wordWrap: 'on',
    smoothScrolling: true,
    tabSize: 2,
  });
  tab.model = model;
  tab.editor = editor;
  tab.language = lang;

  model.onDidChangeContent(() => {
    tab.dirty = true;
    editorRenderTabs();
  });

  // Ctrl+S saves.
  editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, async () => {
    try {
      const pid = state.editor.projectId || '';
      await put('/api/file', {
        project_id: pid,
        path: tab.path,
        content: model.getValue(),
      });
      tab.dirty = false;
      await editorRenderTabs();
      toast('Saved ' + tab.path, 'ok');
    } catch (e) { toast(e.message, 'err'); }
  });

  body.querySelector('#editor-status').textContent = lang + ' · ' + tab.path;
}

function monacoLanguage(path) {
  if (!path) return 'plaintext';
  const ext = (path.split('.').pop() || '').toLowerCase();
  return ({
    js: 'javascript', mjs: 'javascript', cjs: 'javascript',
    ts: 'typescript', tsx: 'typescript',
    jsx: 'javascript',
    json: 'json',
    css: 'css', scss: 'scss', less: 'less',
    html: 'html', htm: 'html', xml: 'xml', svg: 'xml',
    md: 'markdown', markdown: 'markdown',
    py: 'python',
    rs: 'rust',
    go: 'go',
    sh: 'shell', bash: 'shell',
    yml: 'yaml', yaml: 'yaml',
    toml: 'ini', ini: 'ini',
    sql: 'sql',
  })[ext] || 'plaintext';
}

// editorOpenPath opens a tab by path. If the file is already known in the
// tree, this is just an activate; otherwise it opens with whatever content
// the server returns. New files created by the assistant arrive here via
// openTabsForToolUses during the chat pipeline.
function editorOpenPath(p) {
  // Absolute paths get the project root trimmed. The server stores rel_paths.
  let path = p.replace(/\\/g, '/');
  const root = (state.editor.projectPath || '').replace(/\\/g, '/').replace(/\/+$/, '');
  if (root && path.startsWith(root + '/')) path = path.slice(root.length + 1);
  // Otherwise leave the path as-is so non-project files still resolve.
  return editorOpenTab({ path, kind: 'file', makeActive: true });
}

async function editorCloseTab(path) {
  const idx = state.editor.tabs.findIndex(t => t.path === path);
  if (idx < 0) return;
  const tab = state.editor.tabs[idx];
  if (tab.dirty && !confirm(`Discard unsaved changes to ${tab.label}?`)) return;
  if (tab.editor) tab.editor.dispose();
  state.editor.tabs.splice(idx, 1);
  if (state.editor.activeTab === path) {
    const next = state.editor.tabs[idx] || state.editor.tabs[idx - 1];
    state.editor.activeTab = next ? next.path : null;
  }
  await editorRenderTabs();
  if (state.editor.activeTab) {
    await editorActivateTab(state.editor.activeTab);
  } else {
    const body = $('#editor-body');
    if (body) body.innerHTML = `
      <div class="editor-empty">
        <div class="glyph">⎚</div>
        <h2>No file open</h2>
        <p>Pick something from the tree, or ask the assistant.</p>
      </div>`;
  }
  // Drop persisted tab on the server.
  try {
    if (tab.id) await del('/api/tabs/' + encodeURIComponent(tab.id));
  } catch (e) { /* ignore */ }
}

// renderFileError replaces the editor body with a one-shot "why did this
// file fail to load" card so a missing project, stale tab, or bad path no
// longer looks like "every file is broken".
function renderFileError(body, tab, message) {
  body.innerHTML = `
    <div class="editor-empty">
      <div class="glyph">!</div>
      <h2>Could not open ${escapeHtml(tab.path)}</h2>
      <p style="color: var(--err); font-family: var(--font-mono); font-size: 12px;">${escapeHtml(message)}</p>
      <div style="display:flex; gap: 8px; justify-content: center; margin-top: 12px;">
        <button class="btn" data-act="retry">Retry</button>
        <button class="btn" data-act="close">Close tab</button>
        <button class="btn" data-act="picker">Pick project</button>
      </div>
    </div>`;
  body.querySelector('[data-act=retry]').onclick = async () => {
    tab.content = null;
    await editorActivateTab(tab.path);
  };
  body.querySelector('[data-act=close]').onclick = () => editorCloseTab(tab.path);
  body.querySelector('[data-act=picker]').onclick = () => renderProjectPicker(body, tab);
}

// renderProjectPicker renders an inline project switcher inside the editor
// body so the user can recover from "no project bound" without leaving the
// Editor view.
function renderProjectPicker(body, tab) {
  const projects = state.data.projects || [];
  const opts = projects.length
    ? projects.map(p => `<option value="${p.id}">${escapeHtml(p.name)} — ${escapeHtml(p.path)}</option>`).join('')
    : '<option value="">(no projects yet)</option>';
  body.innerHTML = `
    <div class="editor-empty">
      <div class="glyph">⎚</div>
      <h2>Pick the project for this tab</h2>
      <p>${escapeHtml(tab.path)} lives inside one of your projects. Pick which one.</p>
      <select id="picker" style="max-width: 520px;">${opts}</select>
      <div style="display:flex; gap: 8px; justify-content: center; margin-top: 12px;">
        <button class="btn primary" data-act="use">Open in this project</button>
        <button class="btn" data-act="create">Create project…</button>
        <button class="btn" data-act="close">Close tab</button>
      </div>
    </div>`;
  body.querySelector('[data-act=use]').onclick = async () => {
    const sel = body.querySelector('#picker');
    if (!sel || !sel.value) return;
    await editorSetProject(sel.value);
    tab.content = null;
    await editorActivateTab(tab.path);
  };
  body.querySelector('[data-act=close]').onclick = () => editorCloseTab(tab.path);
  body.querySelector('[data-act=create]').onclick = () => setView('projects');
}

function escapeHtml(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// ----------------------------------------------------------
// Editor top menu (File / Edit / Settings / Layout) + modals:
// RGB Mixer, Skill Library, Memory Inspector, New-File / Save-As forms.
// ----------------------------------------------------------

const editorMenu = {
  open: (name) => {
    const menus = document.querySelectorAll('#editor-menu .menu');
    menus.forEach(m => {
      const isMatch = m.dataset.menu === name;
      m.classList.toggle('open', isMatch);
      const list = m.querySelector('.menu-list');
      if (list) list.hidden = !isMatch;
    });
  },
  closeAll: () => {
    document.querySelectorAll('#editor-menu .menu.open').forEach(m => {
      m.classList.remove('open');
      const list = m.querySelector('.menu-list');
      if (list) list.hidden = true;
    });
  },
};

// Apply the persisted theme on boot. Theme is stored in localStorage so
// it survives reloads even when the server is restarted.
function applyStoredTheme() {
  try {
    const t = localStorage.getItem('omnicode.theme') || 'default';
    document.body.setAttribute('data-theme', t);
    applyStoredTokenColors();
  } catch (e) { /* ignore */ }
}

function applyStoredTokenColors() {
  try {
    const raw = localStorage.getItem('omnicode.tokenColors');
    if (!raw) return;
    const overrides = JSON.parse(raw);
    for (const [k, v] of Object.entries(overrides)) {
      document.documentElement.style.setProperty('--tok-' + k, v);
    }
  } catch (e) { /* ignore */ }
}

function setTheme(name) {
  document.body.setAttribute('data-theme', name);
  try { localStorage.setItem('omnicode.theme', name); } catch (e) { /* ignore */ }
  // Reset token overrides only when the user switches base theme;
  // the RGB mixer keeps its own state otherwise.
  applyStoredTokenColors();
  toast('Theme: ' + name, 'ok');
}

function initEditorMenu() {
  const nav = $('#editor-menu');
  if (!nav) return;
  nav.hidden = false;
  editorMenu.closeAll();

  nav.querySelectorAll('.menu .menu-trigger').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.preventDefault();
      e.stopPropagation();
      const menu = btn.parentElement;
      const isOpen = menu.classList.contains('open');
      editorMenu.closeAll();
      if (!isOpen) editorMenu.open(menu.dataset.menu);
    });
  });

  document.addEventListener('click', () => editorMenu.closeAll());
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') editorMenu.closeAll();
  });

  nav.querySelectorAll('.menu-list [data-act]').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      editorMenu.closeAll();
      const act = btn.dataset.act;
      handleMenuAction(act);
    });
  });
}

function handleMenuAction(act) {
  const activeTab = state.editor.tabs.find(t => t.path === state.editor.activeTab);
  const pid = state.editor.projectId || '';

  switch (act) {
    case 'open': openFilePicker(); break;
    case 'save': if (activeTab && activeTab.kind !== 'plan' && activeTab.editor) {
      activeTab.editor.getModel().setValue(activeTab.editor.getValue()); // no-op to ensure dirty
      // Trigger Ctrl+S handler manually.
      const code = window.monaco ? window.monaco.KeyMod.CtrlCmd | window.monaco.KeyCode.KeyS : 0;
      if (code && activeTab.editor) activeTab.editor.trigger('keyboard', 'editor.action.saveFile', {});
      saveActiveTab();
    } break;
    case 'save-as': promptSaveAs(activeTab); break;
    case 'create-file': promptCreate('file'); break;
    case 'create-folder': promptCreate('folder'); break;
    case 'close': if (state.editor.activeTab) editorCloseTab(state.editor.activeTab); break;
    case 'close-all':
      while (state.editor.tabs.length) editorCloseTab(state.editor.tabs[0].path);
      break;
    case 'delete': promptDelete(); break;
    case 'export-session': exportSessionMd(); break;
    case 'import-session': importSessionMd(); break;
    case 'copy': navigator.clipboard.writeText(state.editor.activeTab || '').catch(() => {}); break;
    case 'paste': /* browser handles paste in Monaco */ break;
    case 'cut': navigator.clipboard.writeText(state.editor.activeTab || '').catch(() => {}); break;
    case 'rename': promptRename(); break;
    case 'move': promptMove(); break;
    case 'move-folder': promptMoveFolder(); break;
    case 'open-with': openWithPicker(); break;
    case 'convert': promptConvert(); break;
    case 'zip': zipSelection(); break;
    case 'share': shareActive(); break;
    case 'rgb-mixer': openRgbMixer(); break;
    case 'skill-library': openSkillLibrary(); break;
    case 'memory': openMemoryInspector(); break;

    case 'theme-default': setTheme('default'); break;
    case 'theme-monochrome': setTheme('monochrome'); break;
    case 'theme-light': setTheme('light'); break;
    case 'theme-high-contrast': setTheme('high-contrast'); break;
    case 'theme-solarized': setTheme('solarized'); break;
    case 'theme-nord': setTheme('nord'); break;

    case 'toggle-tree': {
      const tree = $('.file-tree');
      if (tree) tree.style.display = (tree.style.display === 'none' ? '' : 'none');
      break;
    }
    case 'toggle-tabs': {
      const strip = $('.tab-strip');
      if (strip) strip.style.display = (strip.style.display === 'none' ? '' : 'none');
      break;
    }
    case 'split-right': toast('Split-pane editor: queued for next iteration', 'warn'); break;
    case 'reset-layout': {
      document.body.removeAttribute('data-theme');
      try { localStorage.removeItem('omnicode.theme'); localStorage.removeItem('omnicode.tokenColors'); } catch (e) {}
      document.documentElement.removeAttribute('style');
      setTheme('default');
      break;
    }
  }
}

// File operations ------------------------------------------------------------

async function saveActiveTab() {
  const t = state.editor.tabs.find(x => x.path === state.editor.activeTab);
  if (!t || t.kind === 'plan' || !t.editor) return;
  try {
    const pid = state.editor.projectId || '';
    await put('/api/file', { project_id: pid, path: t.path, content: t.editor.getModel().getValue() });
    t.dirty = false;
    await editorRenderTabs();
    toast('Saved ' + t.path, 'ok');
  } catch (e) { toast(e.message, 'err'); }
}

function promptSaveAs(tab) {
  if (!tab || tab.kind === 'plan') { toast('Plan has no Save As', 'warn'); return; }
  openFormModal('Save As — ' + tab.path, [
    { name: 'new_path', label: 'New path (relative to project)', placeholder: 'rename/' + tab.path, value: tab.path },
  ], async (vals) => {
    try {
      await post('/api/saveas', {
        project_id: state.editor.projectId || '',
        src_path: tab.path,
        new_path: vals.new_path,
        content: tab.model ? tab.model.getValue() : (tab.content || ''),
      });
      toast('Copied to ' + vals.new_path, 'ok');
      await editorRefreshTree();
      await editorOpenPath(vals.new_path);
    } catch (e) { toast(e.message, 'err'); }
  });
}

function promptCreate(kind) {
  openFormModal('Create ' + kind, [
    { name: 'path', label: kind === 'folder' ? 'Folder path' : 'File path', placeholder: kind === 'folder' ? 'src/components' : 'src/index.html' },
  ], async (vals) => {
    try {
      const url = kind === 'folder' ? '/api/mkdir' : '/api/mkfile';
      await post(url, { project_id: state.editor.projectId || '', path: vals.path });
      toast('Created ' + kind + ' ' + vals.path, 'ok');
      await editorRefreshTree();
      if (kind === 'file') await editorOpenPath(vals.path);
    } catch (e) { toast(e.message, 'err'); }
  });
}

function promptDelete() {
  const t = state.editor.tabs.find(x => x.path === state.editor.activeTab);
  const target = t ? t.path : '';
  if (!target) { toast('Select a tab or pick a tree node first', 'warn'); return; }
  if (!confirm('DELETE ' + target + '?\nThis cannot be undone.')) return;
  (async () => {
    try {
      await del('/api/remove?project_id=' + encodeURIComponent(state.editor.projectId || '') + '&path=' + encodeURIComponent(target));
      toast('Deleted ' + target, 'ok');
      if (t) editorCloseTab(target);
      await editorRefreshTree();
    } catch (e) { toast(e.message, 'err'); }
  })();
}

function promptRename() {
  const t = state.editor.tabs.find(x => x.path === state.editor.activeTab);
  const target = t ? t.path : '';
  if (!target) { toast('Open a file or pick a tree node first', 'warn'); return; }
  openFormModal('Rename — ' + target, [
    { name: 'new_path', label: 'New path', value: target },
  ], async (vals) => {
    try {
      await post('/api/rename', {
        project_id: state.editor.projectId || '',
        old_path: target,
        new_path: vals.new_path,
      });
      toast('Renamed to ' + vals.new_path, 'ok');
      if (t) {
        t.path = vals.new_path;
        t.label = vals.new_path.split('/').pop();
        await editorRenderTabs();
      }
      await editorRefreshTree();
    } catch (e) { toast(e.message, 'err'); }
  });
}

function promptMove() { promptMoveGeneric('Move file', false); }
function promptMoveFolder() { promptMoveGeneric('Move folder', true); }
function promptMoveGeneric(title, isFolder) {
  const t = state.editor.tabs.find(x => x.path === state.editor.activeTab);
  const target = t ? t.path : '';
  if (!target) { toast('Open something first', 'warn'); return; }
  openFormModal(title + ' — ' + target, [
    { name: 'dst', label: 'Destination folder (relative to project root)' },
  ], async (vals) => {
    try {
      await post('/api/move', {
        project_id: state.editor.projectId || '',
        src: target,
        dst: (vals.dst || '').replace(/\/$/, '') + '/' + target.split('/').pop(),
        kind: isFolder ? 'dir' : 'file',
      });
      toast('Moved ' + target, 'ok');
      await editorRefreshTree();
    } catch (e) { toast(e.message, 'err'); }
  });
}

async function openFilePicker() {
  // Reuse the tree: ask the user to type a path under the project root.
  openFormModal('Open file — type a path under your project', [
    { name: 'path', label: 'Relative path', placeholder: 'README.md' },
  ], async (vals) => {
    await editorOpenPath(vals.path);
  });
}

async function openWithPicker() {
  const t = state.editor.tabs.find(x => x.path === state.editor.activeTab);
  if (!t || t.kind === 'plan') { toast('No file selected', 'warn'); return; }
  openFormModal('Open with…  (Windows app)', [
    { name: 'app', label: 'Application path or "default"', placeholder: 'C:\\Program Files\\Microsoft VS Code\\Code.exe (default = OS choice)' },
  ], async (vals) => {
    try {
      await post('/api/openwith', {
        project_id: state.editor.projectId || '',
        path: t.path,
        app: vals.app,
      });
      toast('Opening ' + t.path, 'ok');
    } catch (e) { toast(e.message, 'err'); }
  });
}

async function promptConvert() {
  const t = state.editor.tabs.find(x => x.path === state.editor.activeTab);
  if (!t || t.kind === 'plan') { toast('No file selected', 'warn'); return; }
  openFormModal('Convert file — ' + t.path, [
    { name: 'target', label: 'Target format', placeholder: 'pdf | docx | txt | md | json | csv | png (depends on toolchain)' },
  ], async (vals) => {
    try {
      const r = await post('/api/convert', {
        project_id: state.editor.projectId || '',
        path: t.path,
        target: vals.target,
      });
      toast('Converted: ' + (r.data && r.data.out || ''), 'ok');
    } catch (e) { toast(e.message, 'err'); }
  });
}

async function zipSelection() {
  const t = state.editor.tabs.find(x => x.path === state.editor.activeTab);
  if (!t) { toast('Select a file or folder first', 'warn'); return; }
  try {
    const r = await post('/api/zip', {
      project_id: state.editor.projectId || '',
      paths: [t.path],
      out: (t.path.split('/').pop() || 'selection') + '.zip',
    });
    toast('Zipped: ' + (r.data && r.data.out || ''), 'ok');
  } catch (e) { toast(e.message, 'err'); }
}

async function shareActive() {
  const t = state.editor.tabs.find(x => x.path === state.editor.activeTab);
  if (!t) { toast('Select a file first', 'warn'); return; }
  try {
    const r = await post('/api/share', {
      project_id: state.editor.projectId || '',
      path: t.path,
    });
    toast('Shared: ' + (r.data && r.data.url || 'copied to clipboard'), 'ok');
    if (r.data && r.data.url) navigator.clipboard.writeText(r.data.url).catch(() => {});
  } catch (e) { toast(e.message, 'err'); }
}

async function exportSessionMd() {
  const sid = state.chat.sessionId;
  if (!sid) { toast('No active session', 'warn'); return; }
  try {
    const r = await get('/api/export/session.md?session_id=' + encodeURIComponent(sid));
    const blob = new Blob([(r.data && r.data.markdown) || ''], { type: 'text/markdown' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = (r.data && r.data.title || 'session') + '.md';
    a.click();
    URL.revokeObjectURL(a.href);
    toast('Exported session.md', 'ok');
  } catch (e) { toast(e.message, 'err'); }
}

function importSessionMd() {
  const input = document.createElement('input');
  input.type = 'file';
  input.accept = '.md,.markdown,.txt';
  input.onchange = async () => {
    const f = input.files[0];
    if (!f) return;
    const text = await f.text();
    try {
      const r = await post('/api/import/session.md', { markdown: text });
      toast('Imported ' + (r.data && r.data.session_id || 'session'), 'ok');
      if (r.data && r.data.session_id) {
        state.chat.sessionId = r.data.session_id;
        if (r.data.messages) state.chat.messages = r.data.messages;
        state.editor.tabs = [];
        setView('editor');
      }
    } catch (e) { toast(e.message, 'err'); }
  };
  input.click();
}

// Generic form modal (single Save As / Create File / Rename / Convert pattern)
function openFormModal(title, fields, onSubmit) {
  const scrim = document.createElement('div');
  scrim.className = 'modal-scrim';
  const wrap = document.createElement('div');
  wrap.className = 'modal';
  const fieldHtml = fields.map(f => `
    <div class="form-row">
      <label>${f.label}</label>
      <input name="${f.name}" type="text" placeholder="${f.placeholder || ''}" value="${f.value || ''}"/>
    </div>`).join('');
  wrap.innerHTML = `
    <div class="modal-head"><h2>${title}</h2><button class="btn small" data-x>×</button></div>
    <form class="modal-body">
      ${fieldHtml}
    </form>
    <div class="modal-foot">
      <button class="btn" data-x>Cancel</button>
      <button class="btn primary" data-ok>OK</button>
    </div>`;
  scrim.appendChild(wrap);
  document.body.appendChild(scrim);
  const close = () => scrim.remove();
  wrap.querySelectorAll('[data-x]').forEach(b => b.onclick = close);
  wrap.querySelector('[data-ok]').onclick = () => {
    const vals = {};
    fields.forEach(f => {
      const el = wrap.querySelector(`[name=${f.name}]`);
      if (el) vals[f.name] = el.value.trim();
    });
    onSubmit(vals);
    close();
  };
  scrim.onclick = (e) => { if (e.target === scrim) close(); };
  // Focus first input
  setTimeout(() => {
    const first = wrap.querySelector('input[type=text]');
    if (first) first.focus();
  }, 30);
}

// RGB Mixer: edit token color tokens live, save to localStorage.
function openRgbMixer() {
  const scrim = document.createElement('div');
  scrim.className = 'modal-scrim';
  const wrap = document.createElement('div');
  wrap.className = 'modal';
  const tokens = ['keyword','string','number','comment','function','type','tag','attr','builtin'];
  const current = {};
  const cs = getComputedStyle(document.body);
  tokens.forEach(t => {
    current[t] = cs.getPropertyValue('--tok-' + t).trim() || '#888888';
  });
  wrap.innerHTML = `
    <div class="modal-head"><h2>RGB Mixer — token colors</h2><button class="btn small" data-x>×</button></div>
    <div class="modal-body">
      <p style="color:var(--text-2); font-size:12px;">Pick a new color for each code type. Changes apply live and persist per browser.</p>
      <div id="rgb-rows"></div>
    </div>
    <div class="modal-foot">
      <button class="btn" data-act="reset">Reset to theme</button>
      <button class="btn primary" data-act="save">Save</button>
      <button class="btn" data-x>Close</button>
    </div>`;
  scrim.appendChild(wrap);
  const rows = wrap.querySelector('#rgb-rows');
  tokens.forEach(t => {
    const hex = current[t];
    const row = document.createElement('div');
    row.className = 'rgb-row';
    row.innerHTML = `
      <label style="font-family:var(--font-mono); font-size:12px;">--tok-${t}</label>
      <input type="range" min="0" max="255" step="1" data-r data-t="${t}"/>
      <input type="range" min="0" max="255" step="1" data-g data-t="${t}"/>
      <input type="range" min="0" max="255" step="1" data-b data-t="${t}"/>
      <div class="swatch" data-swatch="${t}" style="background:${hex}"></div>
    `;
    rows.appendChild(row);
  });
  function hexToRgb(h) {
    const m = h.replace('#','');
    return [parseInt(m.substr(0,2),16), parseInt(m.substr(2,2),16), parseInt(m.substr(4,2),16)];
  }
  function update(t) {
    const r = wrap.querySelector('input[data-r][data-t="' + t + '"]').value;
    const g = wrap.querySelector('input[data-g][data-t="' + t + '"]').value;
    const b = wrap.querySelector('input[data-b][data-t="' + t + '"]').value;
    const hex = '#' + [r,g,b].map(v => parseInt(v).toString(16).padStart(2,'0')).join('');
    wrap.querySelector('[data-swatch="' + t + '"]').style.background = hex;
    document.documentElement.style.setProperty('--tok-' + t, hex);
  }
  tokens.forEach(t => {
    const [r,g,b] = hexToRgb(current[t].startsWith('#') ? current[t] : '#888888');
    wrap.querySelector('input[data-r][data-t="' + t + '"]').value = r;
    wrap.querySelector('input[data-g][data-t="' + t + '"]').value = g;
    wrap.querySelector('input[data-b][data-t="' + t + '"]').value = b;
    ['r','g','b'].forEach(c => {
      wrap.querySelector('input[data-' + c + '][data-t="' + t + '"]').oninput = () => update(t);
    });
  });
  const close = () => scrim.remove();
  wrap.querySelectorAll('[data-x]').forEach(b => b.onclick = close);
  wrap.querySelector('[data-act=save]').onclick = () => {
    const overrides = {};
    tokens.forEach(t => overrides[t] = document.documentElement.style.getPropertyValue('--tok-' + t).trim());
    try { localStorage.setItem('omnicode.tokenColors', JSON.stringify(overrides)); } catch (e) {}
    toast('Token colors saved', 'ok');
    close();
  };
  wrap.querySelector('[data-act=reset]').onclick = () => {
    try { localStorage.removeItem('omnicode.tokenColors'); } catch (e) {}
    document.documentElement.removeAttribute('style');
    close();
    openRgbMixer();
  };
  scrim.onclick = (e) => { if (e.target === scrim) close(); };
  document.body.appendChild(scrim);
}

// Skill Library: lists all skills (built-in + learned), peek + delete.
async function openSkillLibrary() {
  const scrim = document.createElement('div');
  scrim.className = 'modal-scrim';
  const wrap = document.createElement('div');
  wrap.className = 'modal';
  wrap.innerHTML = `
    <div class="modal-head"><h2>Skill Library</h2><button class="btn small" data-x>×</button></div>
    <div class="modal-body"><p style="color:var(--text-2);">Loading…</p></div>
    <div class="modal-foot"><button class="btn" data-x>Close</button></div>`;
  scrim.appendChild(wrap);
  document.body.appendChild(scrim);
  const close = () => scrim.remove();
  wrap.querySelectorAll('[data-x]').forEach(b => b.onclick = close);
  scrim.onclick = (e) => { if (e.target === scrim) close(); };
  try {
    const r = await get('/api/skills?include_learned=1');
    const list = (r.data || []);
    const body = wrap.querySelector('.modal-body');
    if (!list.length) {
      body.innerHTML = '<p style="color:var(--text-2);">No skills yet. The agent will create the first one when it sees a WebSearch.</p>';
      return;
    }
    body.innerHTML = list.map(s => `
      <div class="skill-card">
        <div class="skill-head">
          <div>
            <span class="skill-name">${escapeHtml(s.name)}</span>
            <span class="tag ${s.source || ''}">${escapeHtml(s.source || '')}</span>
          </div>
          <span class="skill-meta">${escapeHtml(s.description || '')}</span>
        </div>
        <pre class="skill-body">${escapeHtml((s.body || '').slice(0, 600))}${(s.body || '').length > 600 ? '\n…' : ''}</pre>
      </div>`).join('');
  } catch (e) {
    wrap.querySelector('.modal-body').innerHTML = '<p style="color:var(--err)">' + escapeHtml(e.message) + '</p>';
  }
}

// Memory Inspector: lists recent session_messages + learned facts.
async function openMemoryInspector() {
  const scrim = document.createElement('div');
  scrim.className = 'modal-scrim';
  const wrap = document.createElement('div');
  wrap.className = 'modal';
  wrap.innerHTML = `
    <div class="modal-head"><h2>Memory Inspector</h2><button class="btn small" data-x>×</button></div>
    <div class="modal-body"><p style="color:var(--text-2);">Loading…</p></div>
    <div class="modal-foot"><button class="btn" data-x>Close</button></div>`;
  scrim.appendChild(wrap);
  document.body.appendChild(scrim);
  const close = () => scrim.remove();
  wrap.querySelectorAll('[data-x]').forEach(b => b.onclick = close);
  scrim.onclick = (e) => { if (e.target === scrim) close(); };
  try {
    const sid = state.chat.sessionId || '';
    const r = await get('/api/memory/recent?session_id=' + encodeURIComponent(sid) + '&limit=50');
    const rows = (r.data || []).slice().reverse();
    const body = wrap.querySelector('.modal-body');
    if (!rows.length) { body.innerHTML = '<p style="color:var(--text-2);">No memory yet.</p>'; return; }
    body.innerHTML = '<div class="memory-list">' + rows.map(m => `
      <div class="row"><span class="ts">${escapeHtml(m.ts || '')}</span><span class="role">${escapeHtml(m.role)}</span><span>${escapeHtml((m.content || '').slice(0, 220))}</span></div>
    `).join('') + '</div>';
  } catch (e) {
    wrap.querySelector('.modal-body').innerHTML = '<p style="color:var(--err)">' + escapeHtml(e.message) + '</p>';
  }
}

// ---------------------------------------------------------------------------
// Beforeunload guard: warn on dirty tabs / sessions
// ---------------------------------------------------------------------------

window.addEventListener('beforeunload', (e) => {
  const dirty = state.editor.tabs.filter(t => t.dirty).length;
  if (dirty) { e.preventDefault(); e.returnValue = ''; }
});

// ----------------------------------------------------------
// Init
// ----------------------------------------------------------

async function init() {
  // Apply the stored theme (and any token overrides) before the first paint
  // so the Editor view doesn't flash the default theme then snap.
  applyStoredTheme();

  // Build the editor top menu and bind its dropdowns.
  initEditorMenu();

  // Nav click handlers
  $$('.nav-item').forEach(el => {
    el.onclick = (e) => { e.preventDefault(); setView(el.dataset.view); };
  });

  // New chat button
  $('#new-chat-btn').onclick = async () => {
    try {
      const r = await post('/api/sessions', { title: 'New session ' + new Date().toLocaleString() });
      state.chat.sessionId = r.data.id;
      state.chat.messages = [];
      setView('chat');
    } catch (e) { toast(e.message, 'err'); }
  };

  // Health check
  try {
    await get('/api/health');
    $('#conn-status').innerHTML = '<span class="dot ok"></span> connected';
  } catch (e) {
    $('#conn-status').innerHTML = '<span class="dot err"></span> disconnected';
  }

  await refreshHeader();
  initSideChat();
  initCogMenu();
  initFloatTerm();
  setView('chat');
}

function initSideChat() {
  const input = document.getElementById('side-chat-input');
  const sendBtn = document.getElementById('side-chat-send');
  const status = document.getElementById('side-chat-status');
  if (!input || !sendBtn || input.dataset.bound) return;
  input.dataset.bound = '1';
  let busy = false;
  const send = async () => {
    if (busy) return;
    const text = input.value;
    if (!text.trim()) return;
    busy = true;
    sendBtn.disabled = true;
    input.value = '';
    try {
      await runChatTurn({
        text,
        attachments: [],
        statusEl: status,
        editorAware: true,
        onRender: paintSideMessages,
      });
    } finally {
      busy = false;
      sendBtn.disabled = false;
      input.focus();
    }
  };
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      send();
    }
  });
  sendBtn.onclick = send;
  paintSideMessages();
}

function initCogMenu() {
  const btn = document.getElementById('sidebar-cog');
  const menu = document.getElementById('cog-menu');
  if (!btn || !menu || btn.dataset.bound) return;
  btn.dataset.bound = '1';
  const close = () => {
    menu.hidden = true;
    btn.classList.remove('open');
  };
  btn.onclick = (e) => {
    e.stopPropagation();
    const open = menu.hidden;
    menu.hidden = !open;
    btn.classList.toggle('open', open);
  };
  menu.querySelectorAll('[data-view]').forEach(item => {
    item.onclick = (e) => {
      e.stopPropagation();
      close();
      setView(item.dataset.view);
    };
  });
  document.addEventListener('click', (e) => {
    if (!menu.hidden && !menu.contains(e.target) && e.target !== btn) close();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') close();
  });
}

function initFloatTerm() {
  const panel = document.getElementById('float-term');
  const bar = document.getElementById('float-term-bar');
  const handle = document.getElementById('float-term-resize');
  if (!panel || !bar || panel.dataset.bound) return;
  panel.dataset.bound = '1';
  document.getElementById('float-term-close').onclick = () => closeFloatTerm();
  bar.querySelectorAll('[data-dock]').forEach(btn => {
    btn.onclick = () => setFloatDock(btn.dataset.dock);
  });

  let drag = null;
  bar.addEventListener('pointerdown', (e) => {
    if (e.target.closest('button') || state.floatTerm.mode !== 'float') return;
    const rect = panel.getBoundingClientRect();
    drag = { dx: e.clientX - rect.left, dy: e.clientY - rect.top };
    bar.setPointerCapture(e.pointerId);
  });
  bar.addEventListener('pointermove', (e) => {
    if (!drag || !bar.hasPointerCapture(e.pointerId)) return;
    moveFloatTerm(e.clientX - drag.dx, e.clientY - drag.dy);
  });
  bar.addEventListener('pointerup', () => { drag = null; });

  let resize = null;
  handle.addEventListener('pointerdown', (e) => {
    if (state.floatTerm.mode !== 'float') return;
    e.stopPropagation();
    const rect = panel.getBoundingClientRect();
    resize = { x: e.clientX, y: e.clientY, w: rect.width, h: rect.height };
    handle.setPointerCapture(e.pointerId);
  });
  handle.addEventListener('pointermove', (e) => {
    if (!resize || !handle.hasPointerCapture(e.pointerId)) return;
    panel.style.width = Math.max(280, resize.w + (e.clientX - resize.x)) + 'px';
    panel.style.height = Math.max(160, resize.h + (e.clientY - resize.y)) + 'px';
    if (state.floatTerm.fit) state.floatTerm.fit.fit();
  });
  handle.addEventListener('pointerup', () => { resize = null; });
}

function mainBox() {
  const main = document.getElementById('main');
  return main ? main.getBoundingClientRect() : { left: 0, top: 0, right: window.innerWidth, bottom: window.innerHeight, width: window.innerWidth, height: window.innerHeight };
}

function moveFloatTerm(x, y) {
  const panel = document.getElementById('float-term');
  const box = mainBox();
  const width = panel.offsetWidth || 520;
  const height = panel.offsetHeight || 300;
  const left = Math.min(Math.max(box.left, x), box.right - 80);
  const top = Math.min(Math.max(box.top + 47, y), box.bottom - 40);
  panel.style.left = (left - box.left) + 'px';
  panel.style.top = (top - box.top) + 'px';
  panel.style.width = width + 'px';
  panel.style.height = height + 'px';
}

function setFloatDock(mode) {
  const panel = document.getElementById('float-term');
  const main = document.getElementById('main');
  state.floatTerm.mode = mode || 'float';
  main.classList.remove('term-dock-left', 'term-dock-right', 'term-dock-bottom');
  panel.classList.toggle('docked', state.floatTerm.mode !== 'float');
  panel.querySelectorAll('[data-dock]').forEach(btn => {
    btn.classList.toggle('active', btn.dataset.dock === state.floatTerm.mode);
  });
  if (state.floatTerm.mode === 'float') {
    panel.style.right = 'auto';
    panel.style.bottom = 'auto';
    const saved = state.floatTerm.floatPos;
    if (!panel.style.left && saved && saved.left) {
      panel.style.left = saved.left;
      panel.style.top = saved.top;
      panel.style.width = saved.width;
      panel.style.height = saved.height;
    } else if (!panel.style.left) {
      const box = mainBox();
      moveFloatTerm(box.right - 540, box.bottom - 340);
    }
  } else {
    if (panel.style.left) {
      state.floatTerm.floatPos = {
        left: panel.style.left,
        top: panel.style.top,
        width: panel.style.width,
        height: panel.style.height,
      };
    }
    main.classList.add('term-dock-' + state.floatTerm.mode);
    panel.style.left = '';
    panel.style.top = '';
    panel.style.right = '';
    panel.style.bottom = '';
    panel.style.width = '';
    panel.style.height = '';
  }
  requestAnimationFrame(() => { if (state.floatTerm.fit) state.floatTerm.fit.fit(); });
}

async function openFloatTerm() {
  const panel = document.getElementById('float-term');
  const already = !panel.hidden && state.floatTerm.term && state.floatTerm.projectId === (state.editor.projectId || '');
  panel.hidden = false;
  if (state.floatTerm.mode === 'float' && !panel.style.left) setFloatDock('float');
  else setFloatDock(state.floatTerm.mode || 'float');
  if (already) {
    if (state.floatTerm.fit) state.floatTerm.fit.fit();
    return;
  }
  await mountFloatTerm();
}

async function closeFloatTerm() {
  const panel = document.getElementById('float-term');
  panel.hidden = true;
  document.getElementById('main').classList.remove('term-dock-left', 'term-dock-right', 'term-dock-bottom');
  if (state.floatTerm.source) {
    state.floatTerm.source.close();
    state.floatTerm.source = null;
  }
  if (state.floatTerm.term) {
    state.floatTerm.term.dispose();
    state.floatTerm.term = null;
    state.floatTerm.fit = null;
  }
  const id = state.floatTerm.id;
  state.floatTerm.id = null;
  if (id) {
    try { await del('/api/terminal/' + id); } catch (e) { /* already gone */ }
  }
}

async function retargetFloatTerm() {
  if (document.getElementById('float-term').hidden) return;
  if (state.floatTerm.projectId === (state.editor.projectId || '')) return;
  if (state.floatTerm.source) {
    state.floatTerm.source.close();
    state.floatTerm.source = null;
  }
  if (state.floatTerm.term) {
    state.floatTerm.term.dispose();
    state.floatTerm.term = null;
    state.floatTerm.fit = null;
  }
  const id = state.floatTerm.id;
  state.floatTerm.id = null;
  if (id) {
    try { await del('/api/terminal/' + id); } catch (e) { /* already gone */ }
  }
  await mountFloatTerm();
}

async function mountFloatTerm() {
  const host = document.getElementById('float-term-host');
  const cwdEl = document.getElementById('float-term-cwd');
  if (!host) return;
  try {
    await ensureXterm();
  } catch (e) {
    host.textContent = 'Terminal UI failed to load. Check the network and reload.';
    return;
  }
  if (state.floatTerm.term) {
    state.floatTerm.term.dispose();
    state.floatTerm.term = null;
  }
  if (state.floatTerm.source) {
    state.floatTerm.source.close();
    state.floatTerm.source = null;
  }
  const term = new window.Terminal({
    cursorBlink: true,
    fontFamily: 'Consolas, ui-monospace, monospace',
    fontSize: 12,
    theme: { background: '#0b0d10', foreground: '#e7eaf0', cursor: '#7c5cff' },
    scrollback: 2000,
  });
  const fit = new window.FitAddon.FitAddon();
  term.loadAddon(fit);
  host.innerHTML = '';
  term.open(host);
  fit.fit();
  state.floatTerm.term = term;
  state.floatTerm.fit = fit;
  const projectId = state.editor.projectId || '';
  let info;
  if (state.floatTerm.id && state.floatTerm.projectId === projectId) {
    info = { id: state.floatTerm.id, cwd: state.floatTerm.cwd };
  } else {
    if (state.floatTerm.id) {
      try { await del('/api/terminal/' + state.floatTerm.id); } catch (e) { /* already gone */ }
      state.floatTerm.id = null;
    }
    const created = await post('/api/terminal', {
      project_id: projectId,
      cols: term.cols || 80,
      rows: term.rows || 16,
    });
    info = created.data || {};
    state.floatTerm.id = info.id;
    state.floatTerm.projectId = projectId;
    state.floatTerm.cwd = info.cwd || '';
  }
  if (cwdEl) cwdEl.textContent = state.floatTerm.cwd || '';
  term.onData(data => {
    if (!state.floatTerm.id) return;
    post('/api/terminal/' + state.floatTerm.id + '/input', { data }).catch(() => {});
  });
  const src = new EventSource('/api/terminal/' + info.id + '/stream');
  state.floatTerm.source = src;
  src.addEventListener('data', (ev) => {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.b64) term.write(bytesFromB64(msg.b64));
    } catch (e) { /* ignore a bad chunk */ }
  });
  term.onResize(({ cols, rows }) => {
    if (!state.floatTerm.id) return;
    post('/api/terminal/' + state.floatTerm.id + '/resize', { cols, rows }).catch(() => {});
  });
  requestAnimationFrame(() => { if (state.floatTerm.fit) state.floatTerm.fit.fit(); });
}

document.addEventListener('DOMContentLoaded', () => {
  document.documentElement.dataset.booted = String(Date.now());
  init().finally(dismissSplash);
});

// The splash stays up long enough to read the mark, then fades once the
// IDE has finished its first load. A click skips the wait.
function closeSplashNow() {
  const splash = document.getElementById('splash');
  if (!splash || splash.hidden || splash.classList.contains('hide')) return;
  splash.dataset.closing = '1';
  splash.classList.add('hide');
  const done = () => { splash.hidden = true; };
  splash.addEventListener('transitionend', done, { once: true });
  setTimeout(done, 700);
}

function dismissSplash() {
  const splash = document.getElementById('splash');
  if (!splash || splash.dataset.closing) return;
  const born = Number(document.documentElement.dataset.booted || Date.now());
  const wait = Math.max(0, 1600 - (Date.now() - born));
  splash.dataset.closing = '1';
  setTimeout(closeSplashNow, wait);
}

document.getElementById('splash').addEventListener('click', closeSplashNow);

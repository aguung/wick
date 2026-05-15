// editor.js — wires Drawflow to the wick workflow editor.
//
// Lifecycle:
//   1. Read base URL + serialized graph from <script id="wf-graph-data">.
//   2. Init Drawflow on #wf-canvas, import existing graph (when present).
//   3. Bind palette drag-source → canvas drop-target.
//   4. Inspector reads/writes the selected node's data.
//   5. Save button serializes Drawflow → POSTs JSON body.
(function () {
  'use strict';

  const root = document.querySelector('[data-wf-base]');
  if (!root) return;
  const baseURL = root.dataset.wfBase;
  const canvasEl = document.getElementById('wf-canvas');
  if (!canvasEl || typeof Drawflow === 'undefined') {
    console.error('[wf] Drawflow lib or canvas missing');
    return;
  }

  const editor = new Drawflow(canvasEl);
  editor.reroute = true;
  editor.editor_mode = 'edit';
  editor.start();

  const dataIsland = document.getElementById('wf-graph-data');
  let initialGraph = null;
  const raw = dataIsland && (dataIsland.dataset.graph || dataIsland.textContent.trim());
  if (raw) {
    try { initialGraph = JSON.parse(raw); }
    catch (err) { console.warn('[wf] graph json parse', err); }
  }
  if (initialGraph && initialGraph.drawflow) {
    editor.import(initialGraph);
  } else {
    seedEmptyGraph();
  }

  // ── Palette → canvas drop ──────────────────────────────────────
  document.querySelectorAll('.wf-palette-item').forEach((el) => {
    el.addEventListener('dragstart', (e) => {
      e.dataTransfer.setData('node-type', el.dataset.nodeType);
      e.dataTransfer.effectAllowed = 'copy';
    });
  });
  canvasEl.addEventListener('dragover', (e) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
  });
  canvasEl.addEventListener('drop', (e) => {
    e.preventDefault();
    const type = e.dataTransfer.getData('node-type');
    if (!type) return;
    const rect = canvasEl.getBoundingClientRect();
    const pos = canvasToFlow(e.clientX - rect.left, e.clientY - rect.top);
    addNodeOfType(type, pos.x, pos.y);
  });

  // ── Inspector ──────────────────────────────────────────────────
  const insEmpty = document.getElementById('inspector-empty');
  const insNode = document.getElementById('inspector-node');
  const f = {
    id: document.getElementById('ins-id'),
    type: document.getElementById('ins-type'),
    label: document.getElementById('ins-label'),
    prompt: document.getElementById('ins-prompt'),
    cases: document.getElementById('ins-cases'),
    preset: document.getElementById('ins-preset'),
    command: document.getElementById('ins-command'),
    url: document.getElementById('ins-url'),
    method: document.getElementById('ins-method'),
    channel: document.getElementById('ins-channel'),
    op: document.getElementById('ins-op'),
    module: document.getElementById('ins-module'),
    connOp: document.getElementById('ins-conn-op'),
    refs: document.getElementById('ins-refs'),
  };
  const panels = {
    prompt: document.getElementById('ins-prompt-panel'),
    cases: document.getElementById('ins-cases-panel'),
    preset: document.getElementById('ins-preset-panel'),
    command: document.getElementById('ins-command-panel'),
    url: document.getElementById('ins-url-panel'),
    channel: document.getElementById('ins-channel-panel'),
    connector: document.getElementById('ins-connector-panel'),
  };
  let selectedID = null;

  editor.on('nodeSelected', (id) => { selectedID = id; showInspectorFor(id); });
  editor.on('nodeUnselected', () => { selectedID = null; hideInspector(); });
  editor.on('nodeRemoved', () => { selectedID = null; hideInspector(); refreshOutputRefs(); });
  editor.on('connectionCreated', () => refreshOutputRefs());

  Object.values(f).forEach((el) => {
    if (!el || el === f.cases || el === f.refs) return;
    el.addEventListener('input', () => { if (selectedID) updateNodeData(selectedID); });
  });

  document.getElementById('ins-add-case').addEventListener('click', () => {
    if (!selectedID) return;
    appendCaseRow('', '');
    persistCases(selectedID);
  });
  document.getElementById('ins-delete').addEventListener('click', () => {
    if (!selectedID) return;
    if (!confirm('Delete this node?')) return;
    editor.removeNodeId('node-' + selectedID);
  });

  // ── Zoom controls ──────────────────────────────────────────────
  document.getElementById('wf-zoom-in').addEventListener('click', () => editor.zoom_in());
  document.getElementById('wf-zoom-out').addEventListener('click', () => editor.zoom_out());
  document.getElementById('wf-zoom-reset').addEventListener('click', () => editor.zoom_reset());

  // ── Bottom tab toggle + collapse ───────────────────────────────
  const bottomBody = document.getElementById('wf-bottom-body');
  const bottomToggle = document.getElementById('wf-bottom-toggle');
  document.querySelectorAll('[data-bottom-tab]').forEach((btn) => {
    btn.addEventListener('click', () => {
      const key = btn.dataset.bottomTab;
      document.querySelectorAll('[data-bottom-tab]').forEach((b) => {
        const on = b === btn;
        b.classList.toggle('border-green-500', on);
        b.classList.toggle('text-green-700', on);
        b.classList.toggle('dark:text-green-400', on);
        b.classList.toggle('border-b-2', on);
        b.classList.toggle('font-medium', on);
      });
      document.querySelectorAll('[data-bottom-panel]').forEach((p) => {
        p.classList.toggle('hidden', p.dataset.bottomPanel !== key);
      });
      // Auto-expand body when a tab is clicked.
      if (bottomBody && bottomBody.classList.contains('hidden')) {
        bottomBody.classList.remove('hidden');
        if (bottomToggle) {
          bottomToggle.textContent = '▾ collapse';
          bottomToggle.dataset.collapsed = 'false';
        }
      }
    });
  });
  if (bottomToggle) {
    bottomToggle.addEventListener('click', () => {
      const collapsed = bottomBody.classList.toggle('hidden');
      bottomToggle.textContent = collapsed ? '▴ expand' : '▾ collapse';
      bottomToggle.dataset.collapsed = collapsed ? 'true' : 'false';
    });
  }

  // ── Save: serialize Drawflow → JSON ────────────────────────────
  document.getElementById('save-form').addEventListener('submit', () => {
    document.getElementById('save-body').value = JSON.stringify(editor.export());
  });

  // ── Helpers ───────────────────────────────────────────────────
  function canvasToFlow(x, y) {
    const zoom = editor.zoom || 1;
    const cx = editor.canvas_x || 0;
    const cy = editor.canvas_y || 0;
    return { x: (x - cx) / zoom, y: (y - cy) / zoom };
  }

  function addNodeOfType(type, x, y) {
    const id = uniqueID(type);
    const meta = nodeMeta(type);
    const html = nodeHTML(meta.head, id, meta.hint);
    editor.addNode(id, meta.inputs, meta.outputs, x, y, 'node-' + meta.cssType, {
      id, type: meta.kind, data: meta.defaults,
    }, html);
    refreshOutputRefs();
  }

  function uniqueID(prefix) {
    let i = 1, id = prefix;
    while (idTaken(id)) { i++; id = `${prefix}-${i}`; }
    return id;
  }
  function idTaken(id) {
    const all = editor.export();
    const nodes = all.drawflow.Home.data;
    return Object.values(nodes).some((n) => n.name === id);
  }
  function nodeHTML(head, title, hint) {
    return `<div class="node-head">${head}</div><div class="node-body"><div class="title">${title}</div><div class="meta">${hint}</div></div>`;
  }

  function nodeMeta(type) {
    const t = type.startsWith('trigger-') ? 'trigger' : type;
    const fixtures = {
      trigger:   { kind: 'trigger', head: 'trigger', hint: type.replace('trigger-', ''), cssType: 'trigger', inputs: 0, outputs: 1, defaults: { triggerKind: type.replace('trigger-', '') } },
      classify:  { kind: 'classify', head: 'classify', hint: 'bug | question | feature', cssType: 'classify', inputs: 1, outputs: 3, defaults: { prompt: '', cases: ['bug', 'question', 'default'] } },
      agent:     { kind: 'agent', head: 'agent', hint: 'reasoning', cssType: 'agent', inputs: 1, outputs: 1, defaults: { prompt: '' } },
      channel:   { kind: 'channel', head: 'channel', hint: 'send_message', cssType: 'channel', inputs: 1, outputs: 1, defaults: { channel: 'slack', op: 'reply_thread' } },
      connector: { kind: 'connector', head: 'connector', hint: 'module · op', cssType: 'connector', inputs: 1, outputs: 1, defaults: { module: '', op: '' } },
      shell:     { kind: 'shell', head: 'shell', hint: 'cmd', cssType: 'shell', inputs: 1, outputs: 1, defaults: { command: [] } },
      http:      { kind: 'http', head: 'http', hint: 'GET / POST', cssType: 'http', inputs: 1, outputs: 1, defaults: { url: '', method: 'GET' } },
      db_query:  { kind: 'db_query', head: 'db_query', hint: 'sql', cssType: 'db_query', inputs: 1, outputs: 1, defaults: { sql: '' } },
      branch:    { kind: 'branch', head: 'branch', hint: 'expr', cssType: 'branch', inputs: 1, outputs: 2, defaults: { expr: '' } },
      parallel:  { kind: 'parallel', head: 'parallel', hint: 'fan-out', cssType: 'parallel', inputs: 1, outputs: 3, defaults: {} },
      end:       { kind: 'end', head: 'end', hint: 'terminator', cssType: 'end', inputs: 1, outputs: 0, defaults: { result: '' } },
      transform: { kind: 'transform', head: 'transform', hint: 'gotemplate', cssType: 'transform', inputs: 1, outputs: 1, defaults: { engine: 'gotemplate', expression: '' } },
    };
    return fixtures[t] || fixtures.shell;
  }

  function seedEmptyGraph() {
    const trig = editor.addNode('trigger', 0, 1, 50, 200, 'node-trigger',
      { id: 'trigger', type: 'trigger', data: { triggerKind: 'manual' } },
      nodeHTML('trigger', 'trigger', 'manual'));
    const end = editor.addNode('end', 1, 0, 420, 200, 'node-end',
      { id: 'end', type: 'end', data: {} },
      nodeHTML('end', 'end', 'terminator'));
    editor.addConnection(trig, end, 'output_1', 'input_1');
  }

  function showInspectorFor(id) {
    insEmpty.classList.add('hidden');
    insNode.classList.remove('hidden');
    const node = editor.getNodeFromId(id);
    if (!node) return;
    const d = node.data || {};
    const kind = d.type || 'shell';
    f.id.textContent = d.id || node.name;
    f.type.textContent = kind;
    f.label.value = node.name || '';
    Object.values(panels).forEach((p) => p.classList.add('hidden'));
    const inner = d.data || {};
    if (kind === 'classify' || kind === 'agent') {
      panels.prompt.classList.remove('hidden');
      panels.preset.classList.remove('hidden');
      f.prompt.value = inner.prompt || '';
      f.preset.value = inner.preset || '';
    }
    if (kind === 'classify') {
      panels.cases.classList.remove('hidden');
      renderCaseRows(inner.cases || []);
    }
    if (kind === 'shell') {
      panels.command.classList.remove('hidden');
      f.command.value = (inner.command || []).join('\n');
    }
    if (kind === 'http') {
      panels.url.classList.remove('hidden');
      f.url.value = inner.url || '';
      f.method.value = inner.method || 'GET';
    }
    if (kind === 'channel') {
      panels.channel.classList.remove('hidden');
      f.channel.value = inner.channel || '';
      f.op.value = inner.op || '';
    }
    if (kind === 'connector') {
      panels.connector.classList.remove('hidden');
      f.module.value = inner.module || '';
      f.connOp.value = inner.op || '';
    }
    refreshOutputRefs();
  }
  function hideInspector() {
    insEmpty.classList.remove('hidden');
    insNode.classList.add('hidden');
  }

  function updateNodeData(id) {
    const node = editor.getNodeFromId(id);
    if (!node) return;
    const d = node.data || {};
    const kind = d.type;
    const newLabel = f.label.value.trim() || (d.id || node.name);
    if (node.html) {
      node.html = node.html.replace(/<div class="title">[^<]*<\/div>/, `<div class="title">${escapeHTML(newLabel)}</div>`);
      const el = document.querySelector(`#node-${id} .title`);
      if (el) el.textContent = newLabel;
    }
    const inner = d.data || {};
    if (kind === 'classify' || kind === 'agent') {
      inner.prompt = f.prompt.value;
      inner.preset = f.preset.value;
    }
    if (kind === 'shell') {
      inner.command = f.command.value.split('\n').filter(Boolean);
    }
    if (kind === 'http') {
      inner.url = f.url.value;
      inner.method = f.method.value;
    }
    if (kind === 'channel') {
      inner.channel = f.channel.value;
      inner.op = f.op.value;
    }
    if (kind === 'connector') {
      inner.module = f.module.value;
      inner.op = f.connOp.value;
    }
    editor.updateNodeDataFromId(id, { id: d.id, type: kind, data: inner });
    refreshOutputRefs();
  }

  function renderCaseRows(cases) {
    f.cases.innerHTML = '';
    (cases.length ? cases : ['default']).forEach((label) => appendCaseRow(label, ''));
  }
  function appendCaseRow(label, target) {
    const row = document.createElement('div');
    row.className = 'flex gap-1';
    row.innerHTML = `
      <input value="${escapeAttr(label)}" placeholder="case" class="flex-1 bg-white border border-slate-300 rounded px-2 py-1"/>
      <input value="${escapeAttr(target)}" placeholder="target" class="flex-1 bg-white border border-slate-300 rounded px-2 py-1 text-slate-600"/>
    `;
    row.querySelectorAll('input').forEach((inp) =>
      inp.addEventListener('input', () => persistCases(selectedID)));
    f.cases.appendChild(row);
  }
  function persistCases(id) {
    if (!id) return;
    const node = editor.getNodeFromId(id);
    if (!node) return;
    const labels = [];
    f.cases.querySelectorAll('div.flex').forEach((row) => {
      const ins = row.querySelectorAll('input');
      if (ins[0] && ins[0].value.trim()) labels.push(ins[0].value.trim());
    });
    const d = node.data || {};
    const inner = d.data || {};
    inner.cases = labels;
    editor.updateNodeDataFromId(id, { id: d.id, type: d.type, data: inner });
  }

  function refreshOutputRefs() {
    if (!f.refs) return;
    const refs = ['{{.Event.Text}}', '{{.Event.User}}', '{{.Event.Channel}}'];
    const all = editor.export();
    const nodes = all.drawflow.Home.data;
    Object.values(nodes).forEach((n) => {
      const nid = (n.data && n.data.id) || n.name;
      if (n.data && n.data.type === 'classify') refs.push(`{{.Node.${nid}.verdict}}`);
      else refs.push(`{{.Node.${nid}.result}}`);
    });
    f.refs.innerHTML = refs.map((r) => `<div>${escapeHTML(r)}</div>`).join('');
  }
  function escapeAttr(s) { return String(s).replace(/"/g, '&quot;'); }
  function escapeHTML(s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }
})();

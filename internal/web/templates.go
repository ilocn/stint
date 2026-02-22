package web

// dashboardHTML is the embedded single-page dashboard served at /.
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>stint</title>
<style>
:root {
  --bg: #0f1117;
  --surface: #1a1d27;
  --border: #2a2d3a;
  --text: #e2e8f0;
  --muted: #718096;
  --green: #48bb78;
  --red: #fc8181;
  --yellow: #f6e05e;
  --cyan: #63b3ed;
  --gray: #a0aec0;
  --font: "SF Mono", "Cascadia Code", "Fira Code", "Consolas", monospace;
}
@media (prefers-color-scheme: light) {
  :root {
    --bg: #f7fafc;
    --surface: #ffffff;
    --border: #e2e8f0;
    --text: #1a202c;
    --muted: #718096;
    --green: #276749;
    --red: #c53030;
    --yellow: #975a16;
    --cyan: #2b6cb0;
    --gray: #4a5568;
  }
}
* { box-sizing: border-box; margin: 0; padding: 0; }
body {
  background: var(--bg);
  color: var(--text);
  font-family: var(--font);
  font-size: 13px;
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}
header {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px 0 16px;
}
header h1 {
  font-size: 16px;
  font-weight: 600;
  letter-spacing: 0.05em;
}
#status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--muted);
  transition: background 0.3s;
}
#status-dot.live { background: var(--green); }
nav.tabs {
  display: flex;
  gap: 0;
  padding: 10px 16px 0 16px;
  border-bottom: 1px solid var(--border);
}
nav.tabs button {
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  color: var(--muted);
  font-family: var(--font);
  font-size: 13px;
  padding: 6px 16px 8px 16px;
  cursor: pointer;
  transition: color 0.15s, border-color 0.15s;
  margin-bottom: -1px;
}
nav.tabs button:hover { color: var(--text); }
nav.tabs button.active {
  color: var(--text);
  border-bottom-color: var(--cyan);
  font-weight: 600;
}
main {
  flex: 1;
  padding: 16px;
  overflow: auto;
}
[hidden] { display: none !important; }
#summary {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 16px;
  padding: 8px 12px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 6px;
}
#summary span { margin-right: 12px; }
#filter-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 12px;
}
.filter-btn {
  background: var(--surface);
  border: 1px solid var(--border);
  color: var(--text);
  font-family: var(--font);
  font-size: 11px;
  padding: 3px 10px;
  border-radius: 12px;
  cursor: pointer;
  transition: background 0.15s, border-color 0.15s, color 0.15s, opacity 0.15s;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  line-height: 1.4;
}
.filter-btn:hover:not(.zero) { border-color: var(--muted); }
.filter-btn.zero { opacity: 0.35; cursor: default; }
.filter-count { font-size: 10px; }
#goals { display: flex; flex-direction: column; gap: 12px; }
.goal-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
}
.goal-header {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--border);
}
.goal-id {
  font-size: 11px;
  color: var(--muted);
  flex-shrink: 0;
}
.goal-text {
  flex: 1;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.goal-status {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 4px;
  border: 1px solid var(--border);
  flex-shrink: 0;
}
.task-table {
  width: 100%;
  border-collapse: collapse;
}
.task-table th {
  text-align: left;
  padding: 6px 14px;
  font-size: 11px;
  color: var(--muted);
  border-bottom: 1px solid var(--border);
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.task-table td {
  padding: 7px 14px;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
.task-table tr:last-child td { border-bottom: none; }
.task-table tr:hover td { background: var(--border); }
.sym { font-size: 14px; width: 20px; text-align: center; }
.sym.done    { color: var(--green); }
.sym.failed  { color: var(--red); }
.sym.blocked { color: var(--yellow); }
.sym.running { color: var(--cyan); }
.sym.pending { color: var(--gray); }
.sym.cancelled { color: var(--gray); }
.task-id    { color: var(--muted); font-size: 11px; }
.task-title { max-width: 280px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.task-agent { color: var(--muted); font-size: 11px; }
.task-repo  { color: var(--muted); font-size: 11px; }
.task-reason { color: var(--muted); font-size: 11px; max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.task-reason--error { color: var(--red); }
.no-tasks { padding: 10px 14px; color: var(--muted); font-size: 12px; }
#updated { font-size: 11px; color: var(--muted); margin-top: 12px; text-align: right; }
/* Logs tab */
#log-viewer {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 10px 12px;
  height: calc(100vh - 160px);
  overflow-y: auto;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
}
/* Input tab */
#input-form {
  max-width: 600px;
}
#input-form-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px 14px;
}
#input-form .label-heading {
  display: block;
  margin-bottom: 6px;
  font-size: 11px;
  font-weight: 500;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
#input-form .helper-text {
  display: block;
  font-size: 11px;
  color: var(--muted);
  margin-bottom: 4px;
}
#input-form .hint-text {
  display: block;
  font-size: 11px;
  color: var(--muted);
  font-style: italic;
  margin-bottom: 8px;
}
#goal-textarea {
  width: 100%;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-family: var(--font);
  font-size: 13px;
  padding: 10px 12px;
  resize: vertical;
  outline: none;
  transition: border-color 0.15s;
  box-sizing: border-box;
}
#goal-textarea:focus { border-color: var(--cyan); }
#textarea-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 12px;
}
#char-counter {
  font-size: 11px;
  color: var(--muted);
}
#submit-btn {
  background: var(--surface);
  border: 1px solid var(--cyan);
  color: var(--cyan);
  border-radius: 6px;
  font-family: var(--font);
  font-size: 13px;
  font-weight: 600;
  padding: 8px 20px;
  cursor: pointer;
  transition: opacity 0.15s, background 0.15s, transform 0.1s;
}
#submit-btn:hover:not(:disabled) { background: color-mix(in srgb, var(--cyan) 10%, var(--surface)); }
#submit-btn:active:not(:disabled) { transform: scale(0.98); }
#submit-btn:disabled { opacity: 0.4; cursor: default; }
#submit-msg {
  margin-top: 12px;
  font-size: 12px;
  min-height: 18px;
  padding: 0;
  opacity: 0;
  transition: opacity 0.2s;
}
#submit-msg.visible {
  opacity: 1;
}
#submit-msg.success {
  border-left: 3px solid var(--green);
  padding: 8px 12px;
  color: var(--green);
  background: color-mix(in srgb, var(--green) 8%, transparent);
}
#submit-msg.error {
  border-left: 3px solid var(--red);
  padding: 8px 12px;
  color: var(--red);
  background: color-mix(in srgb, var(--red) 8%, transparent);
}
</style>
</head>
<body>
<header>
  <div id="status-dot"></div>
  <h1>stint</h1>
</header>
<nav class="tabs">
  <button data-tab="status" class="active">Status</button>
  <button data-tab="logs">Logs</button>
  <button data-tab="input">Input</button>
</nav>
<main>
  <div id="tab-status">
    <div id="summary">Loading...</div>
    <div id="filter-bar"></div>
    <div id="goals"></div>
    <div id="updated"></div>
  </div>
  <div id="tab-logs" hidden>
    <div id="log-viewer"></div>
  </div>
  <div id="tab-input" hidden>
    <div id="input-form">
      <div id="input-form-card">
        <label class="label-heading" for="goal-textarea">New Goal</label>
        <span id="goal-helper" class="helper-text">Describe what you want to accomplish in plain English. Be specific and concise.</span>
        <span id="goal-hint" class="hint-text">e.g. Add pagination to /users endpoint, Fix broken auth flow, Refactor database layer</span>
        <textarea id="goal-textarea" rows="5" placeholder="e.g. Add JWT authentication to the API endpoints..." aria-describedby="goal-helper goal-hint"></textarea>
        <div id="textarea-footer">
          <button id="submit-btn" disabled>Submit Goal</button>
          <span id="char-counter">0 chars</span>
        </div>
      </div>
      <div id="submit-msg"></div>
    </div>
  </div>
</main>

<script>
// ── Tab switching ────────────────────────────────────────────────────────────
const tabBtns = document.querySelectorAll('nav.tabs button');
const tabPanes = {
  status: document.getElementById('tab-status'),
  logs:   document.getElementById('tab-logs'),
  input:  document.getElementById('tab-input'),
};

function showTab(name) {
  tabBtns.forEach(function(btn) {
    btn.classList.toggle('active', btn.getAttribute('data-tab') === name);
  });
  Object.keys(tabPanes).forEach(function(k) {
    tabPanes[k].hidden = (k !== name);
  });
  if (name === 'input' && typeof submitMsg !== 'undefined') {
    submitMsg.textContent = '';
    submitMsg.className = '';
  }
}

tabBtns.forEach(function(btn) {
  btn.addEventListener('click', function() {
    showTab(btn.getAttribute('data-tab'));
  });
});

// ── Input tab ────────────────────────────────────────────────────────────────
const textarea = document.getElementById('goal-textarea');
const submitBtn = document.getElementById('submit-btn');
const submitMsg = document.getElementById('submit-msg');
const charCounter = document.getElementById('char-counter');

function updateCharCounter() {
  const len = textarea.value.length;
  charCounter.textContent = len + ' char' + (len === 1 ? '' : 's');
}

textarea.addEventListener('input', function() {
  submitBtn.disabled = textarea.value.trim() === '';
  updateCharCounter();
});

function showMsg(text, type) {
  submitMsg.textContent = text;
  submitMsg.className = 'visible ' + type;
}

submitBtn.addEventListener('click', function() {
  const text = textarea.value.trim();
  if (!text) return;
  submitBtn.disabled = true;
  submitBtn.textContent = 'Submitting...';
  submitMsg.textContent = '';
  submitMsg.className = '';

  const body = new URLSearchParams();
  body.set('text', text);

  fetch('/api/goals', {method: 'POST', body: body.toString(), headers: {'Content-Type': 'application/x-www-form-urlencoded'}})
    .then(function(r) {
      if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
      return r.json();
    })
    .then(function(g) {
      showMsg('Goal queued: ' + (g.id || ''), 'success');
      textarea.value = '';
      updateCharCounter();
      submitBtn.disabled = true;
      submitBtn.textContent = 'Submit Goal';
      setTimeout(function() { showTab('status'); }, 1200);
    })
    .catch(function(err) {
      showMsg('Error: ' + err.message, 'error');
      submitBtn.disabled = false;
      submitBtn.textContent = 'Submit Goal';
    });
});

// ── Logs tab ─────────────────────────────────────────────────────────────────
const logViewer = document.getElementById('log-viewer');
const MAX_LOG_LINES = 1000;
let logLines = [];
let userScrolledUp = false;

logViewer.addEventListener('scroll', function() {
  const atBottom = logViewer.scrollHeight - logViewer.scrollTop <= logViewer.clientHeight + 4;
  userScrolledUp = !atBottom;
});

function appendLog(line) {
  logLines.push(line);
  if (logLines.length > MAX_LOG_LINES) {
    logLines = logLines.slice(logLines.length - MAX_LOG_LINES);
  }
  logViewer.textContent = logLines.join('\n');
  if (!userScrolledUp) {
    logViewer.scrollTop = logViewer.scrollHeight;
  }
}

// ── Status tab ───────────────────────────────────────────────────────────────
const symbols = {
  done:      '\u2713',
  failed:    '\u2717',
  blocked:   '\u26a0',
  running:   '\u21bb',
  pending:   '\u25cb',
  cancelled: '\u2298',
};

const goalStatuses = ['queued', 'planning', 'active', 'done', 'failed'];
const statusColors = {
  done:     'var(--green)',
  failed:   'var(--red)',
  active:   'var(--cyan)',
  planning: 'var(--yellow)',
  queued:   'var(--gray)',
};

let activeFilter = 'all';

function esc(s) {
  return String(s)
    .replace(/&/g,'&amp;')
    .replace(/</g,'&lt;')
    .replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;');
}

function renderSummary(data) {
  const s = data.summary || {};
  const order = ['done','running','blocked','failed','pending','cancelled'];
  const parts = order
    .filter(function(k) { return s[k] > 0; })
    .map(function(k) { return '<span><b>' + s[k] + '</b> ' + esc(k) + '</span>'; });
  const total = data.total || 0;
  document.getElementById('summary').innerHTML =
    parts.length ? parts.join('') + '<span style="color:var(--muted)">(' + total + ' total)</span>'
                 : '<span style="color:var(--muted)">no tasks</span>';
}

function renderFilterBar(data) {
  const bar = document.getElementById('filter-bar');
  const goals = data.goals || [];

  const counts = {};
  goalStatuses.forEach(function(s) { counts[s] = 0; });
  goals.forEach(function(g) {
    if (Object.prototype.hasOwnProperty.call(counts, g.status)) {
      counts[g.status]++;
    }
  });

  function makeBtn(filter, label, count, color) {
    const isActive = activeFilter === filter;
    const isZero = count === 0 && filter !== 'all';
    let style = '';
    if (isActive && color) {
      style = 'background:' + color + ';color:var(--bg);border-color:' + color + ';';
    }
    return '<button class="filter-btn' + (isActive ? ' active' : '') + (isZero ? ' zero' : '') + '"' +
      ' data-filter="' + esc(filter) + '"' +
      (style ? ' style="' + style + '"' : '') +
      '>' + esc(label) + ' <span class="filter-count">' + count + '</span></button>';
  }

  let html = makeBtn('all', 'All', goals.length, 'var(--text)');
  goalStatuses.forEach(function(s) {
    html += makeBtn(s, s, counts[s], statusColors[s] || 'var(--gray)');
  });

  bar.innerHTML = html;

  bar.querySelectorAll('.filter-btn').forEach(function(btn) {
    btn.addEventListener('click', function() {
      const f = btn.getAttribute('data-filter');
      if (btn.classList.contains('zero')) return;
      activeFilter = f;
      renderFilterBar(data);
      renderGoals(data);
    });
  });
}

function renderGoals(data) {
  const container = document.getElementById('goals');
  const allGoals = data.goals || [];

  const goals = activeFilter === 'all'
    ? allGoals
    : allGoals.filter(function(g) { return g.status === activeFilter; });

  if (goals.length === 0) {
    if (activeFilter !== 'all' && allGoals.length > 0) {
      container.innerHTML = '<p style="color:var(--muted)">No goals with status <b>' + esc(activeFilter) + '</b>.</p>';
    } else {
      container.innerHTML = '<p style="color:var(--muted)">No goals yet.</p>';
    }
    return;
  }

  container.innerHTML = goals.map(function(g) {
    const taskRows = (g.tasks || []).map(function(t) {
      const sym = symbols[t.status] || '?';
      const reasonClass = t.status === 'failed' ? 'task-reason task-reason--error' : 'task-reason';
      const reason = t.reason ? '<span class="' + reasonClass + '">' + esc(t.reason) + '</span>' : '';
      return '<tr>' +
        '<td class="sym ' + esc(t.status) + '">' + esc(sym) + '</td>' +
        '<td class="task-id">' + esc(t.id) + '</td>' +
        '<td class="task-title">' + esc(t.title) + '</td>' +
        '<td class="task-agent">' + esc(t.agent || '--') + '</td>' +
        '<td class="task-repo">' + esc(t.repo || '--') + '</td>' +
        '<td>' + reason + '</td>' +
        '</tr>';
    }).join('');

    const tasksSection = g.tasks && g.tasks.length > 0
      ? '<table class="task-table">' +
          '<thead><tr><th></th><th>Task</th><th>Title</th><th>Agent</th><th>Repo</th><th>Reason</th></tr></thead>' +
          '<tbody>' + taskRows + '</tbody>' +
        '</table>'
      : '<div class="no-tasks">No tasks yet.</div>';

    return '<div class="goal-card">' +
      '<div class="goal-header">' +
        '<span class="goal-id">' + esc(g.id) + '</span>' +
        '<span class="goal-text">' + esc(g.text) + '</span>' +
        '<span class="goal-status">' + esc(g.status) + '</span>' +
      '</div>' +
      tasksSection +
      '</div>';
  }).join('');
}

function render(data) {
  renderSummary(data);
  renderFilterBar(data);
  renderGoals(data);
  const dot = document.getElementById('status-dot');
  dot.classList.add('live');
  const d = new Date(data.updated_at * 1000);
  document.getElementById('updated').textContent =
    'Updated: ' + d.toLocaleTimeString();
}

// ── SSE connection ────────────────────────────────────────────────────────────
// Initial fetch for immediate status on load.
fetch('/api/status')
  .then(function(r) { return r.json(); })
  .then(render)
  .catch(function(e) { console.error('fetch error:', e); });

// Live updates via SSE with named events.
const es = new EventSource('/events');
es.addEventListener('status', function(e) {
  try {
    render(JSON.parse(e.data));
  } catch(err) {
    console.error('SSE status parse error:', err);
  }
});
es.addEventListener('log', function(e) {
  appendLog(e.data);
});
es.onerror = function() {
  document.getElementById('status-dot').classList.remove('live');
};
es.onopen = function() {
  document.getElementById('status-dot').classList.add('live');
};

// ── Log polling fallback ──────────────────────────────────────────────────────
// Poll /api/logs every 30 seconds to ensure logs stay fresh even if SSE
// events stop arriving (e.g. no new writes, dropped messages, connection gaps).
setInterval(function() {
  fetch('/api/logs')
    .then(function(r) { return r.json(); })
    .then(function(lines) {
      if (!Array.isArray(lines)) return;
      // Build a set of lines already displayed for dedup.
      var existing = new Set(logLines);
      var added = false;
      for (var i = 0; i < lines.length; i++) {
        if (!existing.has(lines[i])) {
          logLines.push(lines[i]);
          existing.add(lines[i]);
          added = true;
        }
      }
      if (added) {
        if (logLines.length > MAX_LOG_LINES) {
          logLines = logLines.slice(logLines.length - MAX_LOG_LINES);
        }
        logViewer.textContent = logLines.join('\n');
        if (!userScrolledUp) {
          logViewer.scrollTop = logViewer.scrollHeight;
        }
      }
    })
    .catch(function(e) { console.error('log poll error:', e); });
}, 30000);
</script>
</body>
</html>`

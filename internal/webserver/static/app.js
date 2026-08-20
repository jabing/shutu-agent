// Personal Agent Portal — vanilla JS, no build step (M10a, ADR D-WEB-3).
// Hash-routed SPA: #/sessions (list) → #/sessions/{id} (event stream),
// #/dashboard (M10c: /api/stats rollup with native-DOM bars) and #/kb
// (placeholder page, filled by M10b). The bearer token lives in localStorage
// and rides every fetch; a 401 drops back to the login view.

const KEY = "pa_token";
const app = document.getElementById("app");

function token() { return localStorage.getItem(KEY) || ""; }

async function api(path) {
  const res = await fetch(path, {
    headers: { Authorization: "Bearer " + token() },
  });
  if (res.status === 401) { showLogin("会话已过期，请重新输入令牌"); return null; }
  if (!res.ok) {
    let msg = "HTTP " + res.status;
    try { msg = (await res.json()).error || msg; } catch (_) {}
    throw new Error(msg);
  }
  return res.json();
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, c =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function fmtTime(t) {
  if (!t) return "";
  const d = new Date(t);
  if (isNaN(d.getTime())) return ""; // e.g. the Go zero time
  return d.toLocaleString("zh-CN", { hour12: false });
}

function nav() {
  const cur = location.hash.split("/")[1] || "sessions";
  const links = [["sessions", "会话"], ["dashboard", "工作台"], ["kb", "知识库"]]
    .map(([h, label]) => `<a href="#/${h}" class="${cur === h ? "active" : ""}">${label}</a>`)
    .join("");
  return `<header><h1>Personal Agent</h1><nav>${links}</nav></header>`;
}

function showLogin(msg) {
  localStorage.removeItem(KEY);
  app.innerHTML = `
    ${nav()}
    <div class="panel">
      <h2>登录</h2>
      ${msg ? `<p class="err">${esc(msg)}</p>` : ""}
      <p class="muted">输入 web_server.token 访问门户（令牌只存本机浏览器）</p>
      <input type="password" id="tok" placeholder="Bearer token">
      <button id="go">进入</button>
    </div>`;
  document.getElementById("go").onclick = async () => {
    const t = document.getElementById("tok").value.trim();
    if (!t) return;
    localStorage.setItem(KEY, t);
    try {
      const h = await api("/api/health");
      if (h && h.ok) { location.hash = "#/sessions"; render(); }
    } catch (e) { showLogin(String(e.message || e)); }
  };
}

async function renderSessions() {
  let rows = "";
  try {
    const list = await api("/api/sessions");
    if (list && list.length) {
      rows = list.map(s => `
        <tr>
          <td><a class="rowlink" href="#/sessions/${esc(s.id)}">${esc(s.id)}</a></td>
          <td>${esc(fmtTime(s.created_at))}</td>
          <td>${esc(fmtTime(s.updated_at))}</td>
          <td>${s.event_count}</td>
        </tr>`).join("");
    } else {
      rows = `<tr><td colspan="4" class="muted">暂无会话（启动 REPL 产生会话后可见）</td></tr>`;
    }
  } catch (e) {
    rows = `<tr><td colspan="4" class="err">${esc(String(e.message || e))}</td></tr>`;
  }
  app.innerHTML = `
    ${nav()}
    <div class="panel">
      <h2>会话</h2>
      <table>
        <thead><tr><th>ID</th><th>创建</th><th>更新</th><th>事件数</th></tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>`;
}

async function renderEvents(id) {
  let rows = "";
  try {
    const events = await api("/api/sessions/" + encodeURIComponent(id) + "/events");
    if (events && events.length) {
      rows = events.map(ev => `
        <tr>
          <td class="muted">${ev.seq}</td>
          <td><span class="tag">${esc(ev.type)}</span></td>
          <td class="muted">${esc(fmtTime(ev.time))}</td>
          <td class="event">${esc(ev.summary)}</td>
        </tr>`).join("");
    } else {
      rows = `<tr><td colspan="4" class="muted">无事件</td></tr>`;
    }
  } catch (e) {
    rows = `<tr><td colspan="4" class="err">${esc(String(e.message || e))}</td></tr>`;
  }
  app.innerHTML = `
    ${nav()}
    <p><a class="rowlink" href="#/sessions">← 返回会话列表</a></p>
    <div class="panel">
      <h2>会话 ${esc(id)}</h2>
      <table>
        <thead><tr><th>seq</th><th>类型</th><th>时间</th><th>摘要</th></tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>`;
}

// renderDashboard shows the /api/stats rollup (M10c, D-WEB-5): headline
// counts, last activity, and the event-type distribution as native DOM bars —
// no chart library.
async function renderDashboard() {
  let body = "";
  try {
    const st = await api("/api/stats");
    if (!st) return;
    const counts = st.event_type_counts || {};
    const total = st.events_total || 0;
    const last = (st.last_active && st.events_total > 0) ? fmtTime(st.last_active) : "—";
    const bars = Object.entries(counts)
      .sort((a, b) => b[1] - a[1])
      .map(([type, n]) => {
        const pct = total ? Math.round((n / total) * 100) : 0;
        return `
          <div class="bar-row">
            <span class="tag">${esc(type)}</span>
            <div class="bar-track"><div class="bar" style="width:${pct}%"></div></div>
            <span class="muted bar-n">${n} · ${pct}%</span>
          </div>`;
      }).join("");
    body = `
      <div class="stat-grid">
        <div class="stat"><b>${st.sessions_total}</b><span>会话总数</span></div>
        <div class="stat"><b>${st.events_total}</b><span>事件总数</span></div>
        <div class="stat"><b>${st.tool_calls}</b><span>工具调用</span></div>
      </div>
      <p class="muted">最近活动：<b>${esc(last)}</b></p>
      <h3 class="muted">事件类型分布</h3>
      ${bars || `<p class="muted">暂无事件</p>`}`;
  } catch (e) {
    body = `<p class="err">${esc(String(e.message || e))}</p>`;
  }
  app.innerHTML = `
    ${nav()}
    <div class="panel">
      <h2>工作台</h2>
      ${body}
    </div>`;
}

function renderPlaceholder(title, note) {
  app.innerHTML = `
    ${nav()}
    <div class="panel placeholder">
      <h2>${esc(title)}</h2>
      <p>${esc(note)}</p>
    </div>`;
}

async function render() {
  if (!token()) { showLogin(""); return; }
  const parts = location.hash.split("/");
  const route = parts[1] || "sessions";
  if (route === "sessions") {
    if (parts[2]) await renderEvents(parts[2]);
    else await renderSessions();
  } else if (route === "dashboard") {
    await renderDashboard();
  } else if (route === "kb") {
    renderPlaceholder("知识库", "KB 管理台待 KB 全量后挂（M10b 空壳）。");
  } else {
    location.hash = "#/sessions";
  }
}

window.addEventListener("hashchange", render);
render();

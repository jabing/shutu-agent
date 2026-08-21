/* Shutu AI Agent — dsh-style workspace (P1). Vanilla JS, no build, zero
   dependencies. Ported UI conventions from dsh web (ui-layout / ui-conversation
   / ui-theme). Auth is optional (D-WEB2-G): no token configured → the portal
   serves open; a 401 drops to the login view. */

"use strict";

// ---- storage keys -------------------------------------------------------
const KEY_TOKEN = "pa_token";
const KEY_THEME = "pa_theme";
const KEY_CURRENT = "pa_current";

// ---- layout constants (ui-layout columns.ts) -----------------------------
const SIDEBAR_DEFAULT = 280;
const SIDEBAR_MIN = 264;
const SIDEBAR_MAX = 420;
const SIDEBAR_COLLAPSED = 56;
const SIDEBAR_AUTO_COLLAPSE = 1024;
const CENTER_MIN = 640;

// ---- element refs --------------------------------------------------------
const $ = (id) => document.getElementById(id);
const loginEl = $("login"), loginForm = $("login-form"), loginMsg = $("login-msg");
const workspaceEl = $("workspace"), frameEl = $("frame");
const sessionList = $("session-list"), newSessionBtn = $("new-session");
const curSessionEl = $("cur-session"), modeBadgeEl = $("mode-badge"), modelLabelEl = $("model-label");
const messagesEl = $("messages"), heroEl = $("hero");
const composerText = $("composer-text"), composerBox = $("composer"), sendBtn = $("composer-send");
const growWrapEl = document.querySelector(".grow-wrap");
const scrollBottomBtn = $("scroll-bottom");
const settingsEl = $("settings"), placeholderEl = $("placeholder");

// ---- state ---------------------------------------------------------------
let currentID = localStorage.getItem(KEY_CURRENT) || "";
let layout = { sidebar: SIDEBAR_DEFAULT, manual: false, narrowViewport: false, dragging: false };
let sseAbort = null;            // AbortController for the current session stream
let sseReconnect = null;        // timer handle
let streamState = null;         // {seq, node} for the assistant bubble being built
let runningNode = null;         // "Deep diving..." element
let pollTimer = null;           // session-list refresh
let config = {};                // cached GET /api/config view

// ---- token / api ---------------------------------------------------------
function token() { return localStorage.getItem(KEY_TOKEN) || ""; }

// api performs an authenticated JSON request; a 401 drops to the login view.
async function api(path, opts = {}) {
  const headers = Object.assign({ "Content-Type": "application/json" }, opts.headers || {});
  if (token()) headers.Authorization = "Bearer " + token();
  const res = await fetch(path, Object.assign({}, opts, { headers }));
  if (res.status === 401) { showLogin("令牌无效或已过期"); throw new Error("unauthorized"); }
  return res;
}

// ---- login ---------------------------------------------------------------
function showLogin(msg) {
  loginMsg.textContent = msg || "";
  loginMsg.classList.toggle("hidden", !msg);
  loginEl.classList.remove("hidden");
}
function hideLogin() { loginEl.classList.add("hidden"); }

// ---- theme (P5: light / dark / system, dsh ThemeRuntime) ---------------------
function currentDark() {
  const pref = localStorage.getItem(KEY_THEME) || "system";
  if (pref === "light") return false;
  if (pref === "dark") return true;
  return !!(window.matchMedia && matchMedia("(prefers-color-scheme: dark)").matches);
}
function applyTheme() {
  const dark = currentDark();
  document.documentElement.style.colorScheme = dark ? "dark" : "light";
  document.body.setAttribute("data-ds-dark-theme", dark ? "true" : "false");
  let meta = document.querySelector('meta[name="theme-color"]');
  if (!meta) { meta = document.createElement("meta"); meta.name = "theme-color"; document.head.appendChild(meta); }
  meta.content = dark ? "#151517" : "#FFFFFF";
  // Brand logo: the user's monochrome PNG — white mark on dark, black on light.
  // The rail toggle's brand mark follows the same theme (dsh brand mark slot).
  const logo = $("brand-logo");
  if (logo) logo.src = dark ? "/static/logo_w.png" : "/static/logo_b.png";
  const tlogo = $("toggle-logo");
  if (tlogo) tlogo.src = dark ? "/static/logo_w.png" : "/static/logo_b.png";
  const icon = $("theme-toggle");
  if (icon) icon.textContent = dark ? "☀️" : "🌙";
  const icon2 = $("theme-toggle-settings");
  if (icon2) icon2.textContent = dark ? "☀️" : "🌙";
}
function toggleTheme() {
  const dark = currentDark();
  localStorage.setItem(KEY_THEME, dark ? "light" : "dark");
  applyTheme();
}
function initThemeSystem() {
  if (!window.matchMedia) return;
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    // system follows the OS; a manual preference short-circuits (dsh ThemeRuntime)
    if ((localStorage.getItem(KEY_THEME) || "system") === "system") applyTheme();
  });
}

// ---- layout: frame grid + drag + narrow (dsh ui-layout columns + the
//      sidebar toggle from the logo row) -------------------------------------
// The rail is reached either automatically (viewport < SIDEBAR_AUTO_COLLAPSE)
// or by the manual panel toggle; auto-collapse is the only force when the
// viewport is narrow, manual is the only force otherwise.
function sidebarCollapsed() { return layout.narrowViewport || layout.manual; }
function renderColumns() {
  const collapsed = sidebarCollapsed();
  frameEl.style.gridTemplateColumns =
    (collapsed ? SIDEBAR_COLLAPSED : layout.sidebar) + "px minmax(0, 1fr) 0px";
  frameEl.dataset.sidebarCollapsed = String(collapsed);
  frameEl.dataset.detailsCollapsed = "true";
  const h = document.querySelector(".drag-handle");
  if (h) h.style.left = (collapsed ? SIDEBAR_COLLAPSED : layout.sidebar) + "px";
}
function clampSidebar(v) { return Math.max(SIDEBAR_MIN, Math.min(SIDEBAR_MAX, v)); }
function syncSidebarToggle() {
  const collapsed = sidebarCollapsed();
  const t = $("sidebar-toggle");
  if (t) t.title = collapsed ? "展开侧栏" : "折叠侧栏";
  const b = $("brand");
  if (b) b.title = collapsed ? "展开侧栏" : "新建会话";
}
function toggleSidebar() {
  layout.manual = !sidebarCollapsed();
  renderColumns();
  syncSidebarToggle();
}

function setupDrag() {
  const handle = document.createElement("div");
  handle.className = "drag-handle";
  handle.dataset.side = "sidebar";
  frameEl.appendChild(handle);
  handle.style.left = (sidebarCollapsed() ? SIDEBAR_COLLAPSED : layout.sidebar) + "px";

  let origin = 0, base = layout.sidebar, frame = null;
  handle.addEventListener("pointerdown", (e) => {
    if (sidebarCollapsed()) return; // no handle while collapsed
    e.preventDefault();
    handle.setPointerCapture(e.pointerId);
    origin = e.clientX;
    base = layout.sidebar;
    layout.dragging = true;
    frameEl.dataset.dragging = "true";
    handle.dataset.dragging = "true";
  });
  handle.addEventListener("pointermove", (e) => {
    if (!handle.hasPointerCapture(e.pointerId)) return;
    frame ??= requestAnimationFrame(() => {
      frame = null;
      layout.sidebar = clampSidebar(base + (e.clientX - origin));
      renderColumns();
    });
  });
  const end = () => {
    if (!handle.hasPointerCapture(handle.pointerId)) return;
    handle.releasePointerCapture(handle.pointerId);
    if (frame) { cancelAnimationFrame(frame); frame = null; }
    layout.dragging = false;
    delete frameEl.dataset.dragging;
    delete handle.dataset.dragging;
  };
  handle.addEventListener("pointerup", end);
  handle.addEventListener("pointercancel", end);
}

function setupNarrow() {
  const ro = new ResizeObserver(() => {
    const w = frameEl.clientWidth;
    layout.narrowViewport = w < SIDEBAR_AUTO_COLLAPSE;
    renderColumns();
    syncSidebarToggle();
  });
  ro.observe(frameEl);
}

// ---- utilities ------------------------------------------------------------
function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
function fmtTime(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const now = new Date();
  const pad = (n) => String(n).padStart(2, "0");
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  if (d.toDateString() === now.toDateString()) return hm;
  if (d.getFullYear() === now.getFullYear()) return `${d.getMonth() + 1}月${d.getDate()}日 ${hm}`;
  return `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日 ${hm}`;
}
function msgInner() {
  let inner = messagesEl.querySelector(".messages-inner");
  if (!inner) {
    inner = document.createElement("div");
    inner.className = "messages-inner";
    messagesEl.appendChild(inner);
  }
  return inner;
}

// ---- lightweight markdown -------------------------------------------------
function renderMarkdown(text) {
  const t = esc(text);
  const blocks = [];
  let buf = "";
  // crude fenced code split (keeps pre blocks intact)
  const parts = t.split(/(```[\s\S]*?```)/g);
  for (const p of parts) {
    if (/^```/.test(p)) {
      const code = p.replace(/^```[^\n]*\n?/, "").replace(/```$/, "");
      blocks.push(`<pre><code>${code}</code></pre>`);
    } else if (p) {
      let s = p.replace(/`([^`\n]+)`/g, "<code>$1</code>");
      s = s.replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>");
      s = s.replace(/(^|\n)#{1,4} ([^\n]+)/g, "$1<h3>$2</h3>");
      s = s.replace(/(^|\n)[-*] ([^\n]+)/g, "$1<li>$2</li>");
      s = s.replace(/(^|\n)\d+\. ([^\n]+)/g, "$1<li>$2</li>");
      if (/\n<li>/.test(s)) {
        s = s.replace(/<li>([\s\S]*?)$/, "<ul><li>$1</ul>");
      }
      const paras = s.split(/\n{2,}/).filter((x) => x.trim());
      blocks.push(paras.map((x) => `<p>${x.trim()}</p>`).join(""));
    }
  }
  buf = blocks.join("");
  return buf || esc(text);
}

// ---- message stream --------------------------------------------------------
// addUserMsg renders a user bubble; images (P5) is an optional list of
// {src, id} thumbnails shown above the text.
function addUserMsg(text, timeIso, images) {
  const inner = msgInner();
  const node = document.createElement("div");
  node.className = "msg user";
  let imgs = "";
  if (images && images.length) {
    const cls = images.length === 1 ? "single" : "multi";
    imgs = `<div class="msg-images ${cls}">${images.map((im) => `<img class="msg-image" src="${esc(im.src)}" alt="图片" loading="lazy">`).join("")}</div>`;
  }
  node.innerHTML = `<div class="msg-time">${fmtTime(timeIso)}</div>${imgs}<div class="bubble">${esc(text)}</div>`;
  // failed history images retry once with a cache-busting query
  node.querySelectorAll(".msg-image").forEach((img) => {
    img.addEventListener("error", () => {
      if (img.dataset.retried) return;
      img.dataset.retried = "1";
      img.src = img.src.split("?")[0] + "?retry=" + Date.now();
    });
  });
  inner.appendChild(node);
  scrollToBottom(true);
}

function addRunning() {
  if (runningNode) return;
  const inner = msgInner();
  runningNode = document.createElement("div");
  runningNode.className = "msg running";
  runningNode.innerHTML = `<div class="running-text">Deep diving...</div>`;
  inner.appendChild(runningNode);
  scrollToBottom(true);
}
function removeRunning() {
  if (runningNode) { runningNode.remove(); runningNode = null; }
}

function addReasoning(reasoningText, timeIso) {
  const inner = msgInner();
  const node = document.createElement("div");
  node.className = "msg reasoning";
  const summary = reasoningText.length > 60 ? reasoningText.slice(0, 60) + "…" : reasoningText;
  node.innerHTML = `
    <div class="reasoning-row"><span class="reasoning-caret">▶</span>💭 思考过程
      <span class="reasoning-summary">${esc(summary)}</span></div>
    <div class="reasoning-body">${esc(reasoningText)}</div>`;
  node.querySelector(".reasoning-row").addEventListener("click", () => {
    node.classList.toggle("open");
    scrollToBottom();
  });
  inner.appendChild(node);
}

function addAssistant(text, timeIso, seq) {
  const inner = msgInner();
  const node = document.createElement("div");
  node.className = "msg assistant";
  const fb = seq != null
    ? `<button class="act-btn" data-act="up" title="好">👍</button>
       <button class="act-btn" data-act="down" title="差">👎</button>`
    : "";
  node.innerHTML = `
    <div class="markdown">${text ? renderMarkdown(text) : "<p></p>"}</div>
    <div class="actions-row">
      <button class="act-btn" data-act="copy" title="复制">⧉</button>
      ${fb}
      <span class="act-time">${fmtTime(timeIso)}</span>
    </div>`;
  node.querySelector('[data-act="copy"]').addEventListener("click", () => {
    navigator.clipboard?.writeText(text || "").catch(() => {});
  });
  if (seq != null) {
    // P5 feedback: localStorage per (session, seq) — optimistic, no backend.
    const k = `pa_fb:${currentID || ""}:${seq}`;
    const upBtn = node.querySelector('[data-act="up"]');
    const downBtn = node.querySelector('[data-act="down"]');
    const cur = localStorage.getItem(k);
    if (cur === "up") upBtn.classList.add("active-up");
    if (cur === "down") downBtn.classList.add("active-down");
    const set = (val) => {
      const next = localStorage.getItem(k) === val ? "" : val;
      if (next) localStorage.setItem(k, next); else localStorage.removeItem(k);
      upBtn.classList.toggle("active-up", next === "up");
      downBtn.classList.toggle("active-down", next === "down");
    };
    upBtn.addEventListener("click", () => set("up"));
    downBtn.addEventListener("click", () => set("down"));
  }
  inner.appendChild(node);
  return node.querySelector(".markdown");
}

// appendAssistantStreaming: mutate the live assistant bubble with chunk text.
function appendAssistantStreaming(chunk, seq) {
  let md = streamState && streamState.node;
  if (!md) {
    removeRunning();
    const node = addAssistant("", null, seq);
    streamState = { node };
  }
  streamState.node.append(esc(chunk));
  scrollToBottom(true);
}
function finishAssistant(text, timeIso, seq) {
  removeRunning();
  if (streamState && streamState.node) {
    // replace accumulated DOM text with the final rendered markdown
    streamState.node.innerHTML = text ? renderMarkdown(text) : "<p></p>";
    streamState = null;
  } else if (text) {
    // replay path (snapshot with no streaming chunks): render the bubble fresh
    addAssistant(text, timeIso, seq);
  }
  if (streamActive) { streamActive = false; loadSessions(); }
  scrollToBottom(true);
}

function addToolEvent(ev) {
  const inner = msgInner();
  const node = document.createElement("div");
  const isErr = ev.type === "tool/error";
  node.className = "msg tool" + (isErr ? " error" : "");
  const body = isErr ? ev.summary : ev.tool_output || ev.summary || "（无输出）";
  node.innerHTML = `
    <div class="tool-card">
      <span class="tool-icon">🔧</span>
      <span class="tool-title">${esc(ev.tool_name || "Tool call")}</span>
      <span class="tool-summary">${esc(ev.summary || "")}</span>
      <span class="tool-status-${isErr ? "err" : "ok"}">${isErr ? "✕" : "✓"}</span>
      <span class="tool-caret">▶</span>
    </div>
    <div class="tool-body${isErr ? " error" : ""}">${esc(body)}</div>`;
  node.querySelector(".tool-card").addEventListener("click", () => {
    node.classList.toggle("open");
    scrollToBottom();
  });
  inner.appendChild(node);
  scrollToBottom(true);
}

function addErrorRow(ev) {
  const inner = msgInner();
  const node = document.createElement("div");
  node.className = "msg error";
  node.innerHTML = `<div class="error-row"><span class="error-dot"></span><span>本轮运行失败：${esc(ev.summary || "")}</span></div>`;
  inner.appendChild(node);
  scrollToBottom(true);
}

// ---- scroll behavior -------------------------------------------------------
function nearBottom() {
  return messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight <= 24;
}
function scrollToBottom(force) {
  if (force || nearBottom()) {
    messagesEl.scrollTop = messagesEl.scrollHeight;
    scrollBottomBtn.classList.add("hidden");
  } else {
    scrollBottomBtn.classList.remove("hidden");
  }
}
messagesEl.addEventListener("scroll", () => {
  if (nearBottom()) scrollBottomBtn.classList.add("hidden");
  else scrollBottomBtn.classList.remove("hidden");
});
scrollBottomBtn.addEventListener("click", () => { scrollToBottom(true); });

// ---- session list (P2: dsh ui-workspace single-line rows) -------------------
let searchQuery = "";
let streamActive = false; // a streaming assistant turn is in flight
// dsh ui-workspace search affordance: a section-header icon that expands into
// an inline input (wide), and a 36px rail icon that expands the sidebar and
// lands focus in the input (rail). A non-empty query pins the expansion open.
const wsSearchEl = $("ws-search"), searchToggle = $("search-toggle"),
  searchInput = $("session-search"), searchClear = $("search-clear");
function setSearchExpanded(on) {
  wsSearchEl.classList.toggle("expanded", on);
  searchClear.hidden = !on;
  if (on) { searchInput.tabIndex = 0; searchInput.focus(); }
  else { searchInput.tabIndex = -1; searchInput.blur(); }
}
searchToggle.addEventListener("click", (e) => {
  e.stopPropagation();
  if (sidebarCollapsed()) toggleSidebar();   // rail gesture: expand first
  setSearchExpanded(true);
});
let searchTimer = null;
searchInput.addEventListener("input", (e) => {
  searchQuery = e.target.value.trim();
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => loadSessions(), 250); // debounce (dsh remote search)
});
searchClear.addEventListener("click", (e) => {
  e.stopPropagation();
  searchQuery = ""; searchInput.value = "";
  loadSessions();
  setSearchExpanded(false);
});
searchInput.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  e.stopPropagation();
  searchQuery = ""; searchInput.value = "";
  loadSessions();
  setSearchExpanded(false);
});
document.addEventListener("click", (e) => {
  if (!wsSearchEl.classList.contains("expanded")) return;
  if (e.target.closest("#ws-search")) return;
  if (searchQuery) return; // keep a live filter pinned
  setSearchExpanded(false);
});

// fmtShort: sidebar relative/compact time (dsh ui-workspace relativeTime).
function fmtShort(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const now = new Date();
  const diffMin = Math.round((now - d) / 60000);
  if (diffMin < 1) return "刚刚";
  if (diffMin < 60) return `${diffMin} 分钟前`;
  const diffH = Math.round(diffMin / 60);
  if (diffH < 24) return `${diffH} 小时前`;
  if (d.getFullYear() === now.getFullYear()) return `${d.getMonth() + 1}/${d.getDate()}`;
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`;
}

// ---- P6 workspace grouping (dsh grouped sidebar view) ----------------------
// groupBy is persisted in localStorage like dsh's store; grouped is the
// default (dsh ships grouped). orderBy (manual/updated) mirrors dsh's
// ViewOptionsMenu sort mode. wsGroupOpen remembers per-group collapse.
const GROUP_SESSION_LIMIT = 5;
let groupBy = localStorage.getItem("pa_groupby") || "workspace";
let orderBy = localStorage.getItem("pa_orderby") || "manual";
let wsGroups = [];      // [{id,title,session_ids}]
let wsUngrouped = [];   // ungrouped session ids
let wsGroupOpen = {};
function wsOpenState(key) {
  if (!(key in wsGroupOpen)) wsGroupOpen[key] = localStorage.getItem("pa_ws_g:" + key) !== "0";
  return wsGroupOpen[key];
}
function setWsOpen(key, open) {
  wsGroupOpen[key] = open;
  localStorage.setItem("pa_ws_g:" + key, open ? "1" : "0");
}

async function loadSessions() {
  // dsh section label: 工作区 in grouped views, 会话 in the flat list.
  const label = $("ws-label");
  if (label) label.textContent = groupBy === "flat" ? "会话" : "工作区";
  let res;
  try {
    res = await api("/api/sessions");
  } catch (e) { if (e.message !== "unauthorized") console.error(e); return; }
  const list = await res.json();
  sessionList.textContent = "";
  closeAnyMenu();
  if (!Array.isArray(list) || list.length === 0) {
    const li = document.createElement("li");
    li.className = "session-item";
    li.innerHTML = `<span class="si-title empty">还没有会话，点「新会话」开始</span>`;
    sessionList.appendChild(li);
    return;
  }
  list.sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at));
  // A live query switches to the remote body-text search view (P6.3, dsh
  // searchAcrossSessions); nothing else is drawn while searching.
  if (searchQuery) { doSearch(searchQuery); return; }
  if (groupBy === "flat") { renderFlat(list, ""); return; }
  if (groupBy === "date") { renderDateGroups(list); return; }
  try {
    const wr = await api("/api/workspaces");
    const data = await wr.json();
    wsGroups = data.workspaces || [];
    wsUngrouped = data.ungrouped_ids || [];
  } catch (e) { if (e.message !== "unauthorized") console.error(e); }
  renderGrouped(list);
}

// doSearch fetches body-text hits and draws the search-results view.
async function doSearch(q) {
  let res;
  try {
    res = await api("/api/search?q=" + encodeURIComponent(q));
  } catch (e) { if (e.message !== "unauthorized") console.error(e); return; }
  const data = await res.json();
  const hits = data.hits || [];
  sessionList.textContent = "";
  if (hits.length === 0) {
    const li = document.createElement("li");
    li.className = "session-item";
    li.innerHTML = `<span class="si-title empty">没有找到「${esc(q)}」相关的会话</span>`;
    sessionList.appendChild(li);
    return;
  }
  for (const h of hits) {
    const li = document.createElement("li");
    li.className = "session-item search-hit";
    li.dataset.id = h.id;
    const title = h.title || truncate(h.snippet, 18) || "会话";
    li.innerHTML = `
      <span class="si-dot" data-state="done"></span>
      <span class="sh-main">
        <span class="si-title">${highlight(title, q)}</span>
        <span class="sh-snippet">${highlight(h.snippet, q)}</span>
      </span>
      <span class="si-time">${fmtShort(h.updated_at)}</span>`;
    li.addEventListener("click", () => switchSession(h.id));
    sessionList.appendChild(li);
  }
}

// truncate shortens a string to n chars with an ellipsis (search fallback title).
function truncate(s, n) { return s && s.length > n ? s.slice(0, n) + "…" : s; }
// highlight wraps every case-insensitive occurrence of q in <mark>.
function highlight(text, q) {
  const escT = esc(text || "");
  if (!q) return escT;
  const escQ = esc(q).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return escT.replace(new RegExp(escQ, "gi"), (m) => `<mark>${m}</mark>`);
}

// injectIcons fills every [data-icon] element with the dsh web SVG glyphs
// (icons.js holds the exact paths — user requested the same icons as dsh web).
function injectIcons() {
  const icons = window.PA_ICONS || {};
  document.querySelectorAll("[data-icon]").forEach((el) => {
    const svg = icons[el.dataset.icon];
    if (svg) el.innerHTML = svg;
  });
}

// appendSessionItem appends one session row into a container (shared by the
// flat list and the grouped sublists). dsh SessionNodeItem row contract.
function appendSessionItem(container, s) {
  let state = s.blank ? "idle" : "done";
  if (s.id === currentID && streamActive) state = "running";
  const li = document.createElement("li");
  li.className = "session-item" + (s.id === currentID ? " active" : "");
  li.dataset.id = s.id;
  li.draggable = true;
  li.innerHTML = `
    <span class="si-dot" data-state="${state}"></span>
    <span class="si-title${s.blank ? " empty" : ""}">${esc(s.title || s.id)}</span>
    <span class="si-time">${fmtShort(s.updated_at)}</span>
    <button class="si-menu" title="会话操作">${PA_ICONS.ellipsis}</button>`;
  li.addEventListener("click", (e) => {
    if (e.target.closest(".si-menu")) return;
    switchSession(s.id);
  });
  li.querySelector(".si-menu").addEventListener("click", (e) => {
    e.stopPropagation();
    openMenu(li, s);
  });
  container.appendChild(li);
}

// renderFlat draws the single-list view (dsh FlatList). In manual order a
// user drag establishes flat_sort (manual order wins); otherwise fall back to
// the recently-updated list order. In updated order recent activity always wins.
function renderFlat(list) {
  let arr = list;
  if (orderBy === "manual") {
    const hasManual = list.some((s) => s.flat_sort > 0);
    if (hasManual) arr = [...list].sort((a, b) => a.flat_sort - b.flat_sort);
  }
  for (const s of arr) appendSessionItem(sessionList, s);
}

// renderGrouped draws the dsh grouped tree: a workspace header row per group
// (folder + title + count + hover add/menu) then its session rows; a group
// collapses to its header, and more than GROUP_SESSION_LIMIT rows collapse to
// a 5-row run plus an "expand all" button. The ungrouped bucket keeps its
// sessions but has no workspace actions.
function renderGrouped(list) {
  const byId = new Map(list.map((s) => [s.id, s]));
  const groups = [];
  for (const w of wsGroups) {
    const ids = w.session_ids.filter((id) => byId.has(id));
    // dsh orderBy=updated re-sorts rows by recent activity inside each group;
    // manual order keeps the backend's sort (workspace Sort asc) as-is.
    if (orderBy === "updated") {
      ids.sort((a, b) => new Date(byId.get(b).updated_at) - new Date(byId.get(a).updated_at));
    }
    groups.push({ key: w.id, title: w.title, ws: true, ids });
  }
  const unIds = wsUngrouped.filter((id) => byId.has(id));
  if (unIds.length > 0) groups.push({ key: "__u", title: "未分组", ws: false, ids: unIds });
  let any = false;
  for (const g of groups) {
    if (g.ids.length === 0) continue;
    any = true;
    const wrap = document.createElement("div");
    wrap.className = "ws-group" + (wsOpenState(g.key) ? "" : " closed");
    wrap.dataset.key = g.key;
    const head = document.createElement("button");
    head.className = "group-head";
    head.draggable = true;
    const open = wsOpenState(g.key);
    head.innerHTML = `
      <span class="gh-chevron" aria-hidden="true">${PA_ICONS.triangleright}</span>
      <span class="gh-folder" aria-hidden="true">${g.ws ? (open ? PA_ICONS.folderopen16 : PA_ICONS.folderclose16) : PA_ICONS.folderclose16}</span>
      <span class="gh-title">${esc(g.title)}</span>
      <span class="gh-count">${g.ids.length}</span>
      ${g.ws ? `<span class="gh-actions">
        <span class="gh-act gh-add" title="在此新建会话">${PA_ICONS.plus}</span>
        <span class="gh-act gh-menu" title="工作区操作">${PA_ICONS.ellipsis}</span>
      </span>` : ""}`;
    head.addEventListener("click", (e) => {
      if (e.target.closest(".gh-add") || e.target.closest(".gh-menu")) return;
      const next = !wsOpenState(g.key);
      setWsOpen(g.key, next);
      wrap.classList.toggle("closed", !next);
      head.querySelector(".gh-folder").innerHTML = g.ws
        ? (next ? PA_ICONS.folderopen16 : PA_ICONS.folderclose16)
        : PA_ICONS.folderclose16;
    });
    if (g.ws) {
      head.querySelector(".gh-add").addEventListener("click", async (e) => {
        e.stopPropagation();
        try {
          const res = await api("/api/sessions", {
            method: "POST", body: JSON.stringify({ workspace_id: g.key }),
          });
          const body = await res.json();
          localStorage.setItem(KEY_CURRENT, body.id);
          currentID = body.id;
          await openSession(body.id);
          loadSessions();
        } catch (err) { if (err.message !== "unauthorized") console.error(err); }
      });
      head.querySelector(".gh-menu").addEventListener("click", (e) => {
        e.stopPropagation();
        openWorkspaceMenu(g);
      });
    }
    wrap.appendChild(head);
    if (wsOpenState(g.key)) {
      const ul = document.createElement("ul");
      ul.className = "group-sessions";
      const shown = g.ids.slice(0, GROUP_SESSION_LIMIT);
      for (const id of shown) appendSessionItem(ul, byId.get(id));
      if (g.ids.length > GROUP_SESSION_LIMIT) {
        const ob = document.createElement("button");
        ob.className = "session-overflow";
        ob.textContent = `展开全部会话（${g.ids.length}）`;
        ob.addEventListener("click", () => {
          for (const id of g.ids.slice(GROUP_SESSION_LIMIT)) appendSessionItem(ul, byId.get(id));
          ob.remove();
        });
        ul.appendChild(ob);
      }
      wrap.appendChild(ul);
    }
    sessionList.appendChild(wrap);
  }
  if (!any) {
    const li = document.createElement("li");
    li.className = "session-item";
    li.innerHTML = `<span class="si-title empty">还没有会话，点「新会话」开始</span>`;
    sessionList.appendChild(li);
  }
}

// renderDateGroups draws the date-grouped tree (dsh groupBy=date): 今天 /
// 昨天 / 最近 7 天 / 最近 30 天 / 更早 buckets from updated_at. Buckets have
// no workspace actions — they are pure view grouping, collapsible like the
// workspace headers.
function renderDateGroups(list) {
  const now = new Date();
  const day = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const today = day(now);
  const DAY = 86400000;
  const buckets = [
    { key: "今天", from: today, next: Infinity },
    { key: "昨天", from: today - DAY, next: today },
    { key: "最近 7 天", from: today - 6 * DAY, next: today - DAY },
    { key: "最近 30 天", from: today - 29 * DAY, next: today - 6 * DAY },
    { key: "更早", from: -Infinity, next: today - 29 * DAY },
  ];
  const byBucket = buckets.map((b) => ({ ...b, ids: [] }));
  for (const s of list) {
    const t = new Date(s.updated_at).getTime();
    for (const b of byBucket) {
      if (t >= b.from && t < b.next) { b.ids.push(s.id); break; }
    }
  }
  let any = false;
  for (const b of byBucket) {
    if (b.ids.length === 0) continue;
    any = true;
    const wrap = document.createElement("div");
    wrap.className = "ws-group" + (wsOpenState("d:" + b.key) ? "" : " closed");
    wrap.dataset.key = b.key;
    const head = document.createElement("button");
    head.className = "group-head";
    head.innerHTML = `
      <span class="gh-chevron" aria-hidden="true">${PA_ICONS.triangleright}</span>
      <span class="gh-folder" aria-hidden="true">${PA_ICONS.folderclose16}</span>
      <span class="gh-title">${esc(b.key)}</span>
      <span class="gh-count">${b.ids.length}</span>`;
    head.addEventListener("click", () => {
      setWsOpen("d:" + b.key, !wsOpenState("d:" + b.key));
      wrap.classList.toggle("closed", !wsOpenState("d:" + b.key));
    });
    wrap.appendChild(head);
    if (wsOpenState("d:" + b.key)) {
      const ul = document.createElement("ul");
      ul.className = "group-sessions";
      const byId = new Map(list.map((s) => [s.id, s]));
      for (const id of b.ids) appendSessionItem(ul, byId.get(id));
      wrap.appendChild(ul);
    }
    sessionList.appendChild(wrap);
  }
  if (!any) {
    const li = document.createElement("li");
    li.className = "session-item";
    li.innerHTML = `<span class="si-title empty">还没有会话，点「新会话」开始</span>`;
    sessionList.appendChild(li);
  }
}
function openWorkspaceMenu(g) {
  closeAnyMenu();
  const pop = document.createElement("div");
  pop.className = "si-pop";
  pop.innerHTML = `
    <button data-act="rename">${PA_ICONS.edit}<span>重命名</span></button>
    <button data-act="delete" class="danger">${PA_ICONS.trash}<span>删除工作区</span></button>`;
  pop.addEventListener("click", (e) => {
    const act = e.target.closest("button")?.dataset.act;
    if (!act) return;
    closeAnyMenu();
    if (act === "rename") openWsDialog("rename", g.key, g.title);
    if (act === "delete") deleteWorkspace(g.key);
  });
  document.querySelector(`.ws-group[data-key="${CSS.escape(g.key)}"] .group-head`).appendChild(pop);
  openMenuEl = pop;
}

// ---- P6.2 drag & drop ordering (dsh DnD: workspace rows + session rows) ----
// HTML5 native DnD, no dependencies. Only the grouped view is draggable; the
// flat view keeps its updated_at order (honest boundary). A drop rewrites the
// whole target group's order via PATCH /api/sessions/order so the manual Sort
// is consistent, and dragging a session onto another group's header moves it.
let dragState = null; // {kind:'workspace'|'session', id, groupKey}
let dropPos = null;   // {kind, anchor, el, groupKey}
let dropInd = null;

function ensureDropInd() {
  if (!dropInd) {
    dropInd = document.createElement("div");
    dropInd.className = "drop-indicator";
    document.querySelector(".col-sidebar").appendChild(dropInd);
  }
  return dropInd;
}
function showDropInd(anchorEl, atBottom) {
  const ind = ensureDropInd();
  const col = document.querySelector(".col-sidebar");
  const cr = col.getBoundingClientRect();
  const r = anchorEl.getBoundingClientRect();
  ind.style.top = ((atBottom ? r.bottom : r.top) - cr.top - 2) + "px";
  ind.style.left = "0px";
  ind.style.width = cr.width + "px";
  ind.classList.add("visible");
}
function hideDropInd() { if (dropInd) dropInd.classList.remove("visible"); }

function onDragStart(e) {
  if (groupBy !== "workspace" && groupBy !== "flat") return;
  if (e.target.closest(".gh-act")) return;
  const head = e.target.closest(".group-head");
  const item = e.target.closest(".session-item");
  if (head && head.closest(".ws-group") && head.closest(".ws-group").dataset.key !== "__u") {
    dragState = { kind: "workspace", id: head.closest(".ws-group").dataset.key, groupKey: null };
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", dragState.id);
    return;
  }
  if (item) {
    dragState = {
      kind: "session",
      id: item.dataset.id,
      groupKey: groupBy === "workspace" ? (item.closest(".ws-group")?.dataset.key || "__u") : null,
    };
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", dragState.id);
  }
}

function onDragOver(e) {
  if (!dragState) return;
  e.preventDefault();
  e.dataTransfer.dropEffect = "move";
  const item = e.target.closest(".session-item");
  const head = e.target.closest(".group-head");
  if (dragState.kind === "session" && item) {
    const r = item.getBoundingClientRect();
    const before = e.clientY < r.top + r.height / 2;
    if (groupBy === "flat") {
      dropPos = { kind: "session-flat", anchor: before ? "before" : "after", el: item };
    } else {
      dropPos = { kind: "session", anchor: before ? "before" : "after", el: item, groupKey: item.closest(".ws-group")?.dataset.key || "__u" };
    }
    if (before) showDropInd(item);
    else if (item.nextElementSibling && item.nextElementSibling.classList.contains("session-item")) showDropInd(item.nextElementSibling);
    else showDropInd(item, true);
    return;
  }
  if (dragState.kind === "session" && head) {
    const gkey = head.closest(".ws-group").dataset.key;
    const first = head.parentElement.querySelector(".group-sessions .session-item");
    dropPos = { kind: "session", anchor: "top", el: first, groupKey: gkey };
    if (first) showDropInd(first);
    else showDropInd(head);
    return;
  }
  if (dragState.kind === "workspace" && head && head.closest(".ws-group").dataset.key !== "__u") {
    const wrap = head.closest(".ws-group");
    const r = head.getBoundingClientRect();
    const before = e.clientY < r.top + r.height / 2;
    dropPos = { kind: "workspace", anchor: before ? "before" : "after", el: wrap };
    const next = wrap.nextElementSibling;
    if (before) showDropInd(head);
    else if (next && next.classList.contains("ws-group")) showDropInd(next.querySelector(".group-head"));
    else showDropInd(wrap, true);
    return;
  }
  hideDropInd();
  dropPos = null;
}

async function onDrop(e) {
  e.preventDefault();
  const d = dragState;
  const pos = dropPos;
  hideDropInd();
  dragState = null;
  dropPos = null;
  if (!d || !pos) return;
  try {
    if (d.kind === "workspace" && pos.kind === "workspace") {
      const order = [...sessionList.querySelectorAll(".ws-group")]
        .map((w) => w.dataset.key)
        .filter((k) => k !== "__u");
      const from = order.indexOf(d.id);
      if (from === -1) return;
      order.splice(from, 1);
      const to = order.indexOf(pos.el.dataset.key);
      order.splice(to + (pos.anchor === "after" ? 1 : 0), 0, d.id);
      await api("/api/workspaces/order", { method: "PATCH", body: JSON.stringify({ ids: order }) });
      loadSessions();
      return;
    }
    if (d.kind === "session" && pos.kind === "session-flat") {
      const visible = [...sessionList.querySelectorAll(".session-item")].map((li) => li.dataset.id);
      const at = visible.indexOf(pos.el.dataset.id);
      let idx = pos.anchor === "after" ? at + 1 : at;
      const newOrder = visible.filter((id) => id !== d.id);
      if (at !== -1 && visible.indexOf(d.id) !== -1 && visible.indexOf(d.id) < at) idx -= 1;
      if (idx < 0) idx = 0;
      newOrder.splice(idx, 0, d.id);
      await api("/api/sessions/flat-order", { method: "PATCH", body: JSON.stringify({ ids: newOrder }) });
      loadSessions();
      return;
    }
    if (d.kind === "session") {
      const gkey = pos.groupKey;
      const gEl = sessionList.querySelector(`.ws-group[data-key="${CSS.escape(gkey)}"]`);
      const visible = gEl ? [...gEl.querySelectorAll(".session-item")].map((li) => li.dataset.id) : [];
      const shown = new Set(visible);
      let tail = [];
      if (gkey !== "__u") {
        const w = wsGroups.find((x) => x.id === gkey);
        if (w) tail = w.session_ids.filter((id) => !shown.has(id));
      } else {
        tail = wsUngrouped.filter((id) => !shown.has(id));
      }
      const at = pos.el ? visible.indexOf(pos.el.dataset.id) : -1;
      let idx = pos.anchor === "after" ? at + 1 : at;
      if (pos.anchor === "top") idx = 0;
      const newOrder = visible.filter((id) => id !== d.id);
      if (at !== -1 && visible.indexOf(d.id) !== -1 && visible.indexOf(d.id) < at) idx -= 1;
      if (idx < 0) idx = 0;
      newOrder.splice(idx, 0, d.id);
      const full = newOrder.concat(tail);
      await api("/api/sessions/order", {
        method: "PATCH",
        body: JSON.stringify({ workspace_id: gkey === "__u" ? "" : gkey, session_ids: full }),
      });
      loadSessions();
    }
  } catch (err) { if (err.message !== "unauthorized") console.error(err); }
}

function onDragEnd() {
  hideDropInd();
  dragState = null;
  dropPos = null;
}
sessionList.addEventListener("dragstart", onDragStart);
sessionList.addEventListener("dragover", onDragOver);
sessionList.addEventListener("drop", onDrop);
sessionList.addEventListener("dragend", onDragEnd);

async function deleteWorkspace(id) {
  if (!confirm("删除工作区？其中的会话将移回「未分组」，会话本身不会被删除。")) return;
  try {
    await api(`/api/workspaces/${encodeURIComponent(id)}`, { method: "DELETE" });
  } catch (e) { if (e.message !== "unauthorized") console.error(e); }
  loadSessions();
}

// ---- workspace create / rename dialog (dsh browser-owned Modal) ------------
let wsDialogMode = null; // {mode:'create'} | {mode:'rename', id}
function openWsDialog(mode, id, current) {
  wsDialogMode = mode === "rename" ? { mode: "rename", id } : { mode: "create" };
  $("ws-dialog-title").textContent = mode === "rename" ? "重命名工作区" : "新建工作区";
  $("ws-dialog-ok").textContent = mode === "rename" ? "保存" : "创建";
  const inp = $("ws-dialog-input");
  inp.value = current || "";
  $("ws-dialog").classList.remove("hidden");
  inp.focus();
  inp.select();
}
function closeWsDialog() {
  $("ws-dialog").classList.add("hidden");
  wsDialogMode = null;
}
async function submitWsDialog() {
  if (!wsDialogMode) return;
  const title = $("ws-dialog-input").value.trim();
  if (!title) return;
  try {
    if (wsDialogMode.mode === "rename") {
      await api(`/api/workspaces/${encodeURIComponent(wsDialogMode.id)}`, {
        method: "PATCH", body: JSON.stringify({ title }),
      });
    } else {
      await api("/api/workspaces", { method: "POST", body: JSON.stringify({ title }) });
      groupBy = "workspace";
      localStorage.setItem("pa_groupby", "workspace");
    }
    closeWsDialog();
    loadSessions();
  } catch (e) { if (e.message !== "unauthorized") console.error(e); }
}
$("ws-add").addEventListener("click", () => $("ws-folder").click());
// dsh add-workspace opens a folder dialog; the selected folder's name becomes
// the new workspace title (the workspace itself stays a grouping bucket in the
// local store — no directory is bound yet).
$("ws-folder").addEventListener("change", async () => {
  const f = $("ws-folder").files && $("ws-folder").files[0];
  if (f && f.webkitRelativePath) {
    const folderName = f.webkitRelativePath.split("/")[0];
    try {
      await api("/api/workspaces", { method: "POST", body: JSON.stringify({ title: folderName }) });
      groupBy = "workspace";
      localStorage.setItem("pa_groupby", "workspace");
    } catch (e) { if (e.message !== "unauthorized") console.error(e); }
  }
  $("ws-folder").value = "";
  loadSessions();
});
$("ws-dialog-ok").addEventListener("click", submitWsDialog);
$("ws-dialog-cancel").addEventListener("click", closeWsDialog);
$("ws-dialog-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); submitWsDialog(); }
  if (e.key === "Escape") { e.preventDefault(); closeWsDialog(); }
});
$("ws-dialog").addEventListener("click", (e) => {
  if (e.target === $("ws-dialog")) closeWsDialog();
});
// View-options popover: grouped / flat (dsh ViewOptionsMenu).
const viewMenu = $("view-menu");
$("view-toggle").addEventListener("click", (e) => {
  e.stopPropagation();
  viewMenu.classList.toggle("hidden");
});
viewMenu.addEventListener("click", (e) => {
  const v = e.target.dataset.view;
  const o = e.target.dataset.order;
  if (!v && !o) return;
  if (v) { groupBy = v; localStorage.setItem("pa_groupby", v); }
  if (o) { orderBy = o; localStorage.setItem("pa_orderby", o); }
  viewMenu.classList.add("hidden");
  loadSessions();
});
document.addEventListener("click", (e) => {
  if (!e.target.closest("#view-menu, #view-toggle")) viewMenu.classList.add("hidden");
});

let openMenuEl = null;
function closeAnyMenu() {
  if (openMenuEl) { openMenuEl.remove(); openMenuEl = null; }
  document.querySelectorAll(".session-item.renaming").forEach((el) => el.classList.remove("renaming"));
}
document.addEventListener("click", (e) => { if (!e.target.closest(".si-pop")) closeAnyMenu(); });

function openMenu(li, s) {
  closeAnyMenu();
  const pop = document.createElement("div");
  pop.className = "si-pop";
  // dsh SessionRowMenu: rename / fork / archive (+ delete as a local extra —
  // dsh has no delete UI, this is Shutu AI Agent's own destructive action).
  pop.innerHTML = `
    <button data-act="rename">${PA_ICONS.edit}<span>重命名</span></button>
    <button data-act="fork">${PA_ICONS.branch}<span>派生会话</span></button>
    <button data-act="archive">${PA_ICONS.archive}<span>归档</span></button>
    <button data-act="delete" class="danger">${PA_ICONS.trash}<span>删除会话</span></button>`;
  pop.addEventListener("click", (e) => {
    const act = e.target.closest("button")?.dataset.act;
    if (!act) return;
    closeAnyMenu();
    if (act === "rename") startRename(li, s);
    if (act === "fork") forkSession(s.id);
    if (act === "archive") archiveSession(s.id);
    if (act === "delete") deleteSession(s.id);
  });
  li.appendChild(pop);
  openMenuEl = pop;
}

// forkSession clones the session (POST /api/sessions/{id}/fork, P6.2) and
// switches to the fresh copy.
async function forkSession(id) {
  try {
    const res = await api(`/api/sessions/${encodeURIComponent(id)}/fork`, { method: "POST" });
    const body = await res.json();
    if (!body.id) throw new Error("no id");
    localStorage.setItem(KEY_CURRENT, body.id);
    currentID = body.id;
    await openSession(body.id);
    loadSessions();
  } catch (e) { if (e.message !== "unauthorized") console.error(e); }
}

// archiveSession hides the session from the active tree (P6.2); the log is
// preserved in the store.
async function archiveSession(id) {
  try {
    await api(`/api/sessions/${encodeURIComponent(id)}/archive`, { method: "POST" });
    loadSessions();
  } catch (e) { if (e.message !== "unauthorized") console.error(e); }
}

function startRename(li, s) {
  li.classList.add("renaming");
  const title = li.querySelector(".si-title");
  const old = title.textContent;
  const input = document.createElement("input");
  input.className = "si-rename";
  input.value = s.title || old;
  li.replaceChild(input, title);
  input.focus();
  input.select();
  let done = false;
  const commit = async (save) => {
    if (done) return;
    done = true;
    const val = save ? input.value.trim() : "";
    if (save && val) {
      try {
        await api(`/api/sessions/${encodeURIComponent(s.id)}/title`, {
          method: "PATCH", body: JSON.stringify({ title: val }),
        });
      } catch (e) { if (e.message !== "unauthorized") console.error(e); }
    }
    loadSessions();
  };
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); commit(true); }
    if (e.key === "Escape") { e.preventDefault(); commit(false); }
  });
  input.addEventListener("blur", () => commit(true));
  li.addEventListener("click", (e) => { if (e.target !== input) commit(true); });
}

async function deleteSession(id) {
  const wasCurrent = id === currentID;
  if (!confirm("确定删除这个会话吗？其所有消息将永久移除。")) return;
  try {
    await api(`/api/sessions/${encodeURIComponent(id)}`, { method: "DELETE" });
  } catch (e) { if (e.message !== "unauthorized") console.error(e); }
  if (wasCurrent) {
    if (sseAbort) { sseAbort.abort(); sseAbort = null; }
    streamState = null;
    runningNode = null;
    clearDrafts();
    localStorage.removeItem(KEY_CURRENT);
    currentID = "";
    messagesEl.querySelector(".messages-inner")?.remove();
    curSessionEl.textContent = "";
    heroEl.classList.remove("hidden");
    composerText.disabled = false;
    composerBox.classList.remove("disabled");
    sendBtn.disabled = false;
    updatePlaceholder();
  }
  loadSessions();
}

async function newSession() {
  try {
    const res = await api("/api/sessions", { method: "POST" });
    const body = await res.json();
    if (!body.id) throw new Error("no id");
    localStorage.setItem(KEY_CURRENT, body.id);
    currentID = body.id;
    await openSession(body.id);
    loadSessions();
  } catch (e) { if (e.message !== "unauthorized") console.error(e); }
}

async function switchSession(id) {
  if (id === currentID) return;
  try {
    await api(`/api/sessions/${encodeURIComponent(id)}/resume`, { method: "POST" });
  } catch (e) { if (e.message !== "unauthorized") console.error(e); return; }
  localStorage.setItem(KEY_CURRENT, id);
  currentID = id;
  await openSession(id);
  loadSessions();
}

// ---- session view: messages + SSE ------------------------------------------
function openSession(id) {
  if (sseAbort) { sseAbort.abort(); sseAbort = null; }
  if (sseReconnect) { clearTimeout(sseReconnect); sseReconnect = null; }
  streamState = null;
  runningNode = null;
  streamActive = false;
  messagesEl.querySelector(".messages-inner")?.remove();
  curSessionEl.textContent = id || "";
  heroEl.classList.toggle("hidden", !!id);
  if (!id) return;
  return Promise.all([loadEvents(id), connectStream(id)]);
}

async function loadEvents(id) {
  try {
    const res = await api(`/api/sessions/${encodeURIComponent(id)}/events`);
    const evs = await res.json();
    const inner = msgInner();
    inner.textContent = "";
    let lastTime = "";
    for (const ev of evs) {
      lastTime = ev.time || lastTime;
      renderEvent(ev, true);
    }
    heroEl.classList.add("hidden");
    scrollToBottom(true);
  } catch (e) { if (e.message !== "unauthorized") console.error(e); }
}

function renderEvent(ev, replay) {
  switch (ev.type) {
    case "user/message": {
      const imgs = (ev.images || []).map((iv) => ({
        src: `/api/sessions/${encodeURIComponent(currentID)}/attachments/${iv.id}`,
        id: iv.id,
      }));
      addUserMsg(ev.summary || "", ev.time, imgs.length ? imgs : null);
      break;
    }
    case "assistant/message":
      if (ev.reasoning) addReasoning(ev.reasoning, ev.time);
      finishAssistant(ev.summary || "", ev.time, ev.Seq);
      break;
    case "tool/result":
    case "tool/error":
      if (ev.type === "tool/error" && !ev.tool_name) addErrorRow(ev);
      else addToolEvent(ev);
      break;
    default: break;
  }
}

// connectStream: fetch-based SSE (token stays in the Authorization header;
// EventSource cannot set it — ADR D-WEB2-B).
async function connectStream(id) {
  sseAbort = new AbortController();
  const ac = sseAbort;
  let buf = "";
  const tryConnect = async () => {
    if (ac.signal.aborted) return;
    try {
      const headers = {};
      if (token()) headers.Authorization = "Bearer " + token();
      const res = await fetch(`/api/sessions/${encodeURIComponent(id)}/events/stream`, {
        headers, signal: ac.signal,
      });
      if (res.status === 401) { showLogin("令牌无效或已过期"); return; }
      if (!res.ok || !res.body) return;
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf("\n\n")) !== -1) {
          const frame = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          for (const line of frame.split("\n")) {
            if (line.startsWith("data: ")) {
              try { handleStreamEvent(JSON.parse(line.slice(6))); }
              catch (_) { /* skip malformed frame */ }
            }
          }
        }
      }
    } catch (e) {
      if (ac.signal.aborted) return;
    }
    // stream ended (server closed or network): reconnect after 3s
    if (!ac.signal.aborted && document.visibilityState !== "hidden") {
      sseReconnect = setTimeout(tryConnect, 3000);
    }
  };
  tryConnect();
}

function handleStreamEvent(ev) {
  if (!currentID) return;
  if (ev.type === "assistant/chunk") {
    appendAssistantStreaming(ev.summary || "", ev.Seq);
    return;
  }
  renderEvent(ev, false);
  if (ev.type === "assistant/message") { streamState = null; }
}

// ---- composer ---------------------------------------------------------------
function syncGrow() {
  // The hidden ::after replica layer (grow-wrap) sizes the textarea; it reads
  // the value through the data attribute so only one live source exists.
  growWrapEl.dataset.replicatedValue = composerText.value + "\n";
}
function setComposerDisabled(disabled) {
  composerBox.classList.toggle("disabled", disabled);
  sendBtn.disabled = disabled;
  composerText.disabled = disabled;
}
function placeholderFor() {
  return currentID ? "给智能体发消息…" : "描述你想要构建的内容";
}
function updatePlaceholder() {
  composerText.placeholder = placeholderFor();
}
composerText.addEventListener("input", () => {
  syncGrow();
  updatePlaceholder();
});

async function sendMessage() {
  const text = composerText.value.trim();
  if ((!text && drafts.length === 0) || !currentID) return;
  setComposerDisabled(true);
  try {
    addUserMsg(text, new Date().toISOString(), drafts.length ? drafts.map((d) => ({ src: d.url })) : null);
    addRunning();
    streamActive = true;
    loadSessions(); // blue running dot on the current row
    let images = [];
    if (drafts.length) {
      for (const d of drafts) {
        const id = await uploadDraft(d);
        if (!id) throw new Error("图片上传失败，已保留草稿");
        images.push(id);
      }
      clearDrafts();
    }
    composerText.value = "";
    syncGrow();
    const res = await api(`/api/sessions/${encodeURIComponent(currentID)}/message`, {
      method: "POST",
      body: JSON.stringify({ text, images }),
    });
    if (!res.ok) {
      const body = await res.json().catch(() => ({}));
      throw new Error(body.error || ("HTTP " + res.status));
    }
  } catch (e) {
    if (e.message !== "unauthorized") {
      removeRunning();
      addErrorRow({ summary: e.message });
      console.error(e);
    }
  } finally {
    setComposerDisabled(false);
  }
}

// ---- P5: image attachments (dsh ui-attachment) -------------------------------
const MAX_DRAFTS = 10, MAX_IMG_BYTES = 10 * 1024 * 1024;
const ACCEPTED_TYPES = ["image/png", "image/jpeg", "image/webp", "image/gif"];
let drafts = [];

function draftAcceptable(f) { return ACCEPTED_TYPES.includes(f.type); }
function addDraftFile(f) {
  if (!draftAcceptable(f)) { toast("仅支持 PNG / JPG / WebP / GIF 图片"); return; }
  if (f.size > MAX_IMG_BYTES) { toast("图片超过 10MB"); return; }
  if (drafts.length >= MAX_DRAFTS) { toast("一次最多 10 张图片"); return; }
  drafts.push({ id: (crypto.randomUUID ? crypto.randomUUID() : String(Date.now() + Math.random())), file: f, name: f.name, url: URL.createObjectURL(f) });
  renderDraftRail();
}
function renderDraftRail() {
  let rail = document.querySelector(".draft-rail");
  if (drafts.length === 0) { if (rail) rail.remove(); return; }
  if (!rail) {
    rail = document.createElement("div");
    rail.className = "draft-rail";
    composerBox.closest(".composer-card").before(rail);
  }
  rail.textContent = "";
  for (const d of drafts) {
    const th = document.createElement("div");
    th.className = "draft-thumb";
    th.innerHTML = `<img src="${d.url}" alt="${esc(d.name)}"><button class="draft-remove" title="移除">✕</button>`;
    th.querySelector(".draft-remove").addEventListener("click", (e) => { e.stopPropagation(); removeDraft(d); });
    th.addEventListener("click", () => openLightbox(d.url));
    rail.appendChild(th);
  }
  const count = document.createElement("span");
  count.className = "draft-count";
  count.textContent = `${drafts.length}/${MAX_DRAFTS}`;
  rail.appendChild(count);
}
function removeDraft(d) {
  drafts = drafts.filter((x) => x !== d);
  URL.revokeObjectURL(d.url);
  renderDraftRail();
}
function clearDrafts() {
  for (const d of drafts) URL.revokeObjectURL(d.url);
  drafts = [];
  renderDraftRail();
}
async function uploadDraft(d) {
  const fd = new FormData();
  fd.append("file", d.file, d.name);
  const headers = {};
  if (token()) headers.Authorization = "Bearer " + token();
  const res = await fetch(`/api/sessions/${encodeURIComponent(currentID)}/attachments`, { method: "POST", headers, body: fd });
  if (res.status === 401) { showLogin("令牌无效或已过期"); throw new Error("unauthorized"); }
  if (!res.ok) return null;
  const body = await res.json();
  return body.id || null;
}

// paste + whole-page drop (dsh has no upload button — only these two paths)
composerText.addEventListener("paste", (e) => {
  const files = e.clipboardData ? [...e.clipboardData.files] : [];
  const imgs = files.filter(draftAcceptable);
  if (imgs.length) { e.preventDefault(); for (const f of imgs) addDraftFile(f); }
});
let dragDepth = 0;
function hasImageFiles(dt) {
  return dt && dt.types && dt.types.includes("Files") &&
    [...(dt.items || [])].some((i) => i.kind === "file" && i.type.startsWith("image/"));
}
document.addEventListener("dragover", (e) => {
  if (!hasImageFiles(e.dataTransfer)) return;
  e.preventDefault();
  dragDepth++;
  showDropOverlay();
});
document.addEventListener("dragleave", (e) => {
  dragDepth = Math.max(0, dragDepth - 1);
  if (dragDepth === 0) hideDropOverlay();
});
document.addEventListener("drop", (e) => {
  dragDepth = 0;
  hideDropOverlay();
  if (!hasImageFiles(e.dataTransfer)) return;
  e.preventDefault();
  for (const f of e.dataTransfer.files) addDraftFile(f);
});
function showDropOverlay() {
  let ov = document.querySelector(".drop-overlay");
  if (!ov) { ov = document.createElement("div"); ov.className = "drop-overlay"; ov.textContent = "松开以添加图片"; document.body.appendChild(ov); }
}
function hideDropOverlay() { document.querySelector(".drop-overlay")?.remove(); }

// lightbox (original-size preview)
function openLightbox(src) {
  const lb = document.createElement("div");
  lb.className = "lightbox";
  lb.innerHTML = `<img src="${esc(src)}" alt="原图"><button class="lb-close" title="关闭">✕</button>`;
  const close = () => lb.remove();
  lb.addEventListener("click", (e) => { if (e.target === lb || e.target.classList.contains("lb-close")) close(); });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") close(); }, { once: true });
  document.body.appendChild(lb);
}

// mini toast
let toastTimer = null;
function toast(msg) {
  let t = document.querySelector(".toast");
  if (!t) { t = document.createElement("div"); t.className = "toast"; document.body.appendChild(t); }
  t.textContent = msg;
  t.classList.add("show");
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove("show"), 2600);
}
composerText.addEventListener("keydown", (e) => {
  // Composer send key follows the General-settings preference: "send" sends on
  // plain Enter (Shift+Enter newline); "newline" sends on Ctrl/Cmd+Enter only.
  const mode = localStorage.getItem("pa_enter") || "send";
  const isSend = mode === "send"
    ? (e.key === "Enter" && !e.shiftKey && !e.isComposing)
    : (e.key === "Enter" && (e.ctrlKey || e.metaKey) && !e.isComposing);
  if (isSend) {
    e.preventDefault();
    sendMessage();
  }
});
sendBtn.addEventListener("click", sendMessage);

// ---- topbar / config ----------------------------------------------------------
// loadConfigLabels fills the topbar model/mode badges from the cached config.
function loadConfigLabels() {
  modelLabelEl.textContent = (config.model || "") + (config.llm_provider ? " · " + config.llm_provider : "");
  modeBadgeEl.textContent = config.mode || "";
  modeBadgeEl.classList.toggle("hidden", !config.mode);
  modeBadgeEl.classList.remove("mode-minimal", "mode-code");
  if (config.mode === "minimal") modeBadgeEl.classList.add("mode-minimal");
  if (config.mode === "code") modeBadgeEl.classList.add("mode-code");
}
async function loadConfig() {
  try {
    const res = await api("/api/config");
    config = await res.json();
    loadConfigLabels();
  } catch (e) { if (e.message !== "unauthorized") console.error(e); }
}

// ---- settings page (P3: dsh SettingsRoot two-column panel, read-only) -------
// Section registry: general / model / caps / tools. Every control is read-only
// (ADR D-WEB2-D: no runtime editing — config changes need a restart).
const SETTINGS_SECTIONS = [
  { id: "general", label: "通用设置", icon: "⚙" },
  { id: "model", label: "模型", icon: "◈" },
  { id: "caps", label: "能力开关", icon: "⚡" },
  { id: "tools", label: "工具", icon: "🧰" },
];
const CAPABILITY_NAMES = {
  terminal: "终端", fs: "文件系统", fs_search: "全文检索", ralph: "Ralph 循环",
  workflow: "工作流", kb: "知识库", jobs: "后台任务", subagent: "子代理",
  web: "联网", eval: "评测", code: "代码执行", interact: "交互确认",
  mcp: "MCP", skill: "技能", schedule: "定时", plan: "计划",
  spill: "溢出", compaction: "压缩", multimodal: "多模态",
};
const MODEL_DISPLAY = { "deepseek-chat": "DeepSeek Chat", "deepseek-reasoner": "DeepSeek Reasoner" };
const PROVIDER_DISPLAY = { deepseek: "DeepSeek" };

let settingsSec = "general";
let settingsConfig = null;

function settingsSectionEl() { return $("settings-sec"); }

function rowHTML(title, desc, control) {
  return `<div class="row">
    <div class="row-text"><div class="row-title">${esc(title)}</div>
    ${desc ? `<div class="row-desc">${esc(desc)}</div>` : ""}</div>
    ${control ? `<div class="row-control">${control}</div>` : ""}</div>`;
}

function renderSettingsNav() {
  const nav = $("settings-nav");
  nav.textContent = "";
  for (const s of SETTINGS_SECTIONS) {
    const btn = document.createElement("button");
    btn.className = "nav-cell" + (s.id === settingsSec ? " active" : "");
    btn.setAttribute("aria-current", s.id === settingsSec ? "true" : "false");
    btn.innerHTML = `<span class="nav-ico">${s.icon}</span><span>${esc(s.label)}</span>`;
    btn.addEventListener("click", () => { settingsSec = s.id; renderSettingsNav(); renderSettingsSec(); });
    nav.appendChild(btn);
  }
}

function renderGeneral(c) {
  const pref = localStorage.getItem(KEY_THEME) || "system";
  const cube = (id, label, icon) =>
    `<button class="theme-cube${pref === id ? " selected" : ""}" data-theme="${id}">${icon}<span>${label}</span></button>`;
  // dsh AppearanceRow: title above, the three theme cubes below.
  const appearance = `<div class="appearance-group">
    <div class="row-title">外观</div>
    <div class="theme-cubes">${cube("light", "浅色", PA_ICONS.light)}${cube("dark", "深色", PA_ICONS.dark)}${cube("system", "跟随系统", PA_ICONS.followsystem)}</div>
  </div>`;
  const enterMode = localStorage.getItem("pa_enter") || "send";
  // General-settings rows backed by the durable settings table (PATCH
  // /api/settings, applied at startup → restart required, D-WEB2-D). The
  // selectors fall back to localStorage while the API round-trip completes.
  const sel = (id, cur, opts) =>
    `<select id="${id}" class="row-select">${opts.map(([v, label]) => `<option value="${v}"${cur === v ? " selected" : ""}>${label}</option>`).join("")}</select>`;
  const ap = localStorage.getItem("pa_agent_preset") || "standard";
  const pp = localStorage.getItem("pa_permission_preset") || "standard";
  const ts = localStorage.getItem("pa_terminal_shell") || "off";
  const sec = settingsSectionEl();
  sec.innerHTML = `<h2>通用设置</h2>` +
    appearance +
    // dsh LanguageRow: title + selector pill (English is planned, not shipped).
    `<div class="settings-row">
      <div class="row-text"><div class="row-title">语言</div></div>
      <select id="lang-select" class="row-select">
        <option value="zh" selected>中文</option>
        <option value="en" disabled>English（规划中）</option>
      </select>
    </div>` +
    // dsh AgentPresetRow (数驼语义): the mode preset new sessions compose from.
    `<div class="settings-row">
      <div class="row-text"><div class="row-title">Agent 预设</div><div class="row-desc">新会话默认模式（极简 / 标准 / 编程），重启后生效。</div></div>
      ${sel("agent-preset-select", ap, [["minimal", "极简 minimal"], ["standard", "标准 standard"], ["code", "编程 code"]])}
    </div>` +
    // dsh PermissionRow (数驼语义): the tool-whitelist tier for new sessions.
    `<div class="settings-row">
      <div class="row-text"><div class="row-title">权限</div><div class="row-desc">新会话默认工具权限（只读 / 标准 / 全部），重启后生效。</div></div>
      ${sel("permission-select", pp, [["readonly", "只读"], ["standard", "标准"], ["full", "全部"]])}
    </div>` +
    // Default terminal (dsh 通用设置): pick the shell (Powershell / Git Bash
    // / WSL). Any choice except "关闭" enables the persistent terminal (M9).
    `<div class="settings-row">
      <div class="row-text"><div class="row-title">默认终端</div><div class="row-desc">选择终端使用的 shell（PowerShell / Git Bash / WSL），重启后生效。</div></div>
      ${sel("terminal-select", ts, [["off", "关闭"], ["powershell", "PowerShell"], ["gitbash", "Git Bash"], ["wsl", "WSL"]])}
    </div>` +
    // dsh EnterBehaviorRow: title + description + selector pill.
    `<div class="settings-row">
      <div class="row-text"><div class="row-title">回车发送</div><div class="row-desc">Enter 直接发送，Shift+Enter 换行；或改为 Ctrl+Enter 发送。</div></div>
      <select id="enter-select" class="row-select">
        <option value="send"${enterMode === "send" ? " selected" : ""}>Enter 发送</option>
        <option value="newline"${enterMode === "newline" ? " selected" : ""}>Ctrl+Enter 发送</option>
      </select>
    </div>` +
    `<p class="notice">配置文件：config.yaml —— 修改后重启生效（无运行时热改）。</p>`;
  sec.querySelectorAll(".theme-cube").forEach((b) => {
    b.addEventListener("click", () => {
      localStorage.setItem(KEY_THEME, b.dataset.theme);
      applyTheme();
      renderGeneral(c);
    });
  });
  const enter = sec.querySelector("#enter-select");
  if (enter) enter.addEventListener("change", (e) => { localStorage.setItem("pa_enter", e.target.value); });
  // Durably persist the three host-backed rows on change.
  [["#agent-preset-select", "agent_preset"], ["#permission-select", "permission_preset"], ["#terminal-select", "terminal_shell"]]
    .forEach(([q, key]) => {
      const el = sec.querySelector(q);
      if (!el) return;
      el.addEventListener("change", async () => {
        localStorage.setItem("pa_" + key, el.value);
        try {
          const res = await api("/api/settings", { method: "PATCH", body: JSON.stringify({ [key]: el.value }) });
          if (res.status === 401) { showLogin("令牌无效或已过期"); return; }
          if (!res.ok) throw new Error("HTTP " + res.status);
          renderGeneral(c); // reflect the saved value
        } catch (e) { console.error("save setting", key, e); }
      });
    });
  // Backfill the stored values (and the in-effect values) from the backend.
  (async () => {
    try {
      const res = await api("/api/settings");
      const d = await res.json();
      if (d.agent_preset && sec.querySelector("#agent-preset-select")) sec.querySelector("#agent-preset-select").value = d.agent_preset;
      if (d.permission_preset && sec.querySelector("#permission-select")) sec.querySelector("#permission-select").value = d.permission_preset;
      if (d.terminal_shell && sec.querySelector("#terminal-select")) sec.querySelector("#terminal-select").value = d.terminal_shell;
    } catch (e) { if (e.message !== "unauthorized") console.error("load settings", e); }
  })();
}

// Model settings page (M11: dsh ModelsSection) — provider row-cards, one per
// known provider: every built-in (deepseek always; openai/anthropic even when
// their env key is absent) plus every M11 custom OpenAI-compatible provider.
// Each row shows the display name, a 自定义 tag for custom providers, the active
// tag, a credential dot (configured = a key is present in settings or env →
// green, missing → red), the configured model, and actions: 编辑 (registered
// provider, opens model + key editor), 增加 (dormant built-in, opens the key-only
// setup card), 删除 (custom only). The 增加提供方 button opens a picker of the
// existing dormant built-in providers — dsh's add flow: choose a provider, then
// just enter its API key. 增加自定义提供方 declares a brand-new OpenAI-compatible
// endpoint. Keys default from the environment variable; a key entered here takes
// precedence (配置后以配置的为准, user 2026-09).
const PROVIDER_ENV = { deepseek: "DEEPSEEK_API_KEY", openai: "OPENAI_API_KEY", anthropic: "ANTHROPIC_API_KEY" };
let modelEditing = null;   // provider id currently open in its full editor card
let addingPicker = false;  // true while the 增加提供方 provider picker is open
let addingKeyId = null;    // provider id currently open in its key-only setup card
let customAdding = false;  // true while the 增加自定义提供方 create card is open

function renderModel(c) {
  const sec = settingsSectionEl();
  const providers = (c.providers || []).slice();
  const currentProvider = c.llm_provider || "deepseek";
  const currentModel = c.model || "";
  // configured-first (dsh sorts usable providers up), then registered; the
  // active one keeps its place among the configured rows.
  const sorted = providers.sort((a, b) => (Number(b.configured) - Number(a.configured)) || (Number(b.registered) - Number(a.registered)));
  const envName = (id) => PROVIDER_ENV[id] || (id.toUpperCase().replace(/-/g, "_") + "_API_KEY");
  // Dormant built-in providers = known but not yet registered (no key): these
  // are the dsh "addable" providers offered by 增加提供方.
  const dormant = sorted.filter((p) => !p.custom && !p.registered);

  let t = `<h2>模型</h2>
    <p class="intro">配置 API Key 后即可使用以下提供方。切换提供方 / 模型即时生效（下一条消息即用新模型）；Key 默认从环境变量读取，在本页填入的 Key 以配置值为准（覆盖环境变量）。</p>
    <ul class="m-rows">`;

  for (const p of sorted) {
    const name = PROVIDER_DISPLAY[p.id] || p.name || p.id;
    const active = p.id === currentProvider;
    if (modelEditing === p.id) {
      // Full editor card (dsh rowCard→editor swap): model + base URL + API Key.
      const candOpts = (p.candidates || []).map((m) => `<option value="${esc(m)}">${esc(MODEL_DISPLAY[m] || m)}</option>`).join("");
      const curModel = active ? currentModel : (p.model || "");
      t += `<li class="m-rowcard m-editing">
        <div class="m-editor">
          <div class="m-editorhead">
            <span class="m-editortitle">编辑 ${esc(name)}</span>
            <span class="m-editorroute">${esc(p.id)}</span>
          </div>
          <div class="m-field">
            <span class="m-fieldlabel">模型 ID</span>
            <input id="m-model-name" class="m-input" list="m-model-candidates" value="${esc(curModel)}" placeholder="输入或从建议中选择">
            <datalist id="m-model-candidates">${candOpts}</datalist>
          </div>
          <div class="m-field">
            <span class="m-fieldlabel">API 地址</span>
            <span class="m-fieldvalue">${esc(p.base_url || "提供方默认")}</span>
          </div>
          <div class="m-field">
            <span class="m-fieldlabel">API Key</span>
            <input id="m-provider-key" class="m-input" type="password" autocomplete="off" placeholder="留空使用环境变量 ${esc(envName(p.id))}" value="">
            <span class="m-fieldhint">Key 默认读取环境变量 ${esc(envName(p.id))}；填入后以配置值为准（留空并保存即清除自定义 Key，回到环境变量）。</span>
          </div>
          ${p.registered && !p.available ? `<p class="m-notice">当前不可用（Key 缺失或 API 地址无效）。</p>` : ""}
          <div class="m-editoractions">
            <button type="button" class="m-btn m-secondary" id="m-model-cancel">取消</button>
            <button type="button" class="m-btn m-primary" id="m-model-apply">应用</button>
            <span id="m-model-status" class="model-status"></span>
          </div>
        </div>
      </li>`;
    } else {
      t += `<li class="m-rowcard">
        <div class="m-rowhead">
          <span class="m-rowid">
            <span class="m-rowname">${esc(name)}</span>
            ${active ? `<span class="m-rowtag current">当前</span>` : ""}
            ${p.custom ? `<span class="m-rowtag custom">自定义</span>` : ""}
            <span class="m-dot ${p.configured ? "configured" : "missing"}" title="${p.configured ? "API Key 已配置" : "未配置（缺 API Key）"}"></span>
          </span>
          <span class="m-rowmodel muted">${esc(p.model || "")}</span>
          <span class="m-rowactions">
            ${p.registered
              ? `<button type="button" class="m-btn m-secondary" data-edit="${esc(p.id)}">编辑</button>`
              : `<button type="button" class="m-btn m-secondary" data-addkey="${esc(p.id)}">增加</button>`}
            ${p.custom ? `<button type="button" class="m-btn m-secondary m-danger" data-del="${esc(p.id)}">删除</button>` : ""}
          </span>
        </div>
      </li>`;
    }
  }

  // 增加提供方 picker: choose an existing dormant built-in provider.
  if (addingPicker) {
    t += `<li class="m-rowcard m-editing">
      <div class="m-editor">
        <div class="m-editorhead">
          <span class="m-editortitle">增加提供方</span>
          <span class="m-editorroute">选择已有提供方</span>
        </div>
        ${dormant.length
          ? `<p class="m-notice">选择要启用的提供方，然后只需输入 API Key。</p>
             <div class="m-picklist">` + dormant.map((p) => `
               <button type="button" class="m-btn m-secondary m-pick" data-pick="${esc(p.id)}">
                 <span>${esc(PROVIDER_DISPLAY[p.id] || p.id)}</span>
                 <span class="m-pickhint">${esc(envName(p.id))}</span>
               </button>`).join("") + `</div>`
          : `<p class="m-notice">没有可增加的内置提供方（已全部配置）。可改用「增加自定义提供方」。</p>`}
        <div class="m-editoractions">
          <button type="button" class="m-btn m-secondary" id="m-model-cancel">取消</button>
        </div>
      </div>
    </li>`;
  }

  // Key-only setup card for a picked dormant provider (dsh addCard): just the
  // API key — the model/base URL come from the built-in defaults.
  if (addingKeyId) {
    const p = sorted.find((x) => x.id === addingKeyId);
    const name = p ? (PROVIDER_DISPLAY[p.id] || p.name || p.id) : addingKeyId;
    const model = p ? p.model : "";
    const base = p ? (p.base_url || "提供方默认") : "";
    t += `<li class="m-rowcard m-editing">
      <div class="m-editor">
        <div class="m-editorhead">
          <span class="m-editortitle">增加 ${esc(name)}</span>
          <span class="m-editorroute">${esc(addingKeyId)}</span>
        </div>
        <div class="m-field">
          <span class="m-fieldlabel">模型</span>
          <span class="m-fieldvalue">${esc(model || "提供方默认")}</span>
        </div>
        <div class="m-field">
          <span class="m-fieldlabel">API 地址</span>
          <span class="m-fieldvalue">${esc(base)}</span>
        </div>
        <div class="m-field">
          <span class="m-fieldlabel">API Key</span>
          <input id="m-addkey-value" class="m-input" type="password" autocomplete="off" value="" placeholder="默认读取环境变量 ${esc(envName(addingKeyId))}">
          <span class="m-fieldhint">留空使用环境变量 ${esc(envName(addingKeyId))}；填入后以配置值为准。</span>
        </div>
        <div class="m-editoractions">
          <button type="button" class="m-btn m-secondary" id="m-model-cancel">取消</button>
          <button type="button" class="m-btn m-primary" id="m-addkey-save">保存</button>
          <span id="m-model-status" class="model-status"></span>
        </div>
      </div>
    </li>`;
  }

  // 增加自定义提供方 create card (rendered after the rows, above the add row).
  if (customAdding) {
    t += `<li class="m-rowcard m-editing">
      <div class="m-editor">
        <div class="m-editorhead">
          <span class="m-editortitle">增加自定义提供方</span>
          <span class="m-editorroute">OpenAI 兼容端点</span>
        </div>
        <div class="m-field">
          <span class="m-fieldlabel">路由 ID</span>
          <input id="m-custom-route" class="m-input" value="my-provider" placeholder="如 ollama、vllm（小写字母/数字/-）">
        </div>
        <div class="m-field">
          <span class="m-fieldlabel">显示名称</span>
          <input id="m-custom-name" class="m-input" value="" placeholder="如 Ollama">
        </div>
        <div class="m-field">
          <span class="m-fieldlabel">API 地址</span>
          <input id="m-custom-base" class="m-input" value="" placeholder="https://api.example.com/v1">
        </div>
        <div class="m-field">
          <span class="m-fieldlabel">模型 ID</span>
          <input id="m-custom-model" class="m-input" value="" placeholder="如 gpt-4o-mini">
        </div>
        <div class="m-field">
          <span class="m-fieldlabel">API Key</span>
          <input id="m-custom-key" class="m-input" type="password" autocomplete="off" value="" placeholder="留空使用环境变量 [ROUTE]_API_KEY">
          <span class="m-fieldhint">Key 默认读取环境变量（大写路由名 + _API_KEY，如 OLLAMA_API_KEY）；填入后以配置值为准。</span>
        </div>
        <div class="m-editoractions">
          <button type="button" class="m-btn m-secondary" id="m-model-cancel">取消</button>
          <button type="button" class="m-btn m-primary" id="m-model-apply">创建</button>
          <span id="m-model-status" class="model-status"></span>
        </div>
      </div>
    </li>`;
  }

  // dsh addButton row: 增加提供方 (opens the picker) + 增加自定义提供方.
  t += `</ul>
    <div class="m-addrow">
      <button type="button" class="m-btn m-add" id="m-add-provider">增加提供方</button>
      <button type="button" class="m-btn m-add" id="m-add-custom">增加自定义提供方</button>
    </div>
    <p class="notice">API Key 默认从环境变量读取（不落 config.yaml）；本页填入的 Key 以配置值为准并持久化保存。修改 config.yaml 重启后回到文件配置。</p>`;
  sec.innerHTML = t;

  // Open the full editor card for a registered provider row.
  sec.querySelectorAll("[data-edit]").forEach((btn) => {
    btn.addEventListener("click", () => { modelEditing = btn.dataset.edit; addingPicker = false; addingKeyId = null; customAdding = false; renderModel(c); });
  });
  // Open the key-only setup card for a dormant built-in row.
  sec.querySelectorAll("[data-addkey]").forEach((btn) => {
    btn.addEventListener("click", () => { addingKeyId = btn.dataset.addkey; modelEditing = null; addingPicker = false; customAdding = false; renderModel(c); });
  });
  // Pick a provider in the 增加提供方 picker → key-only setup card.
  sec.querySelectorAll("[data-pick]").forEach((btn) => {
    btn.addEventListener("click", () => { addingKeyId = btn.dataset.pick; addingPicker = false; renderModel(c); });
  });
  // Delete a custom provider (dsh remove, with confirm).
  sec.querySelectorAll("[data-del]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const id = btn.dataset.del;
      if (!confirm(`删除自定义提供方 ${id}？`)) return;
      try {
        const res = await api("/api/config/provider", { method: "DELETE", body: JSON.stringify({ id }) });
        if (res.status === 401) { showLogin("令牌无效或已过期"); return; }
        if (!res.ok) { const eb = await res.json().catch(() => ({})); throw new Error(eb.error || ("HTTP " + res.status)); }
        if (modelEditing === id) modelEditing = null;
        await loadConfig();
        renderModel(config);
      } catch (e) { alert("删除失败：" + e.message); }
    });
  });
  const addProvider = sec.querySelector("#m-add-provider");
  if (addProvider) {
    addProvider.addEventListener("click", () => {
      addingPicker = true; modelEditing = null; addingKeyId = null; customAdding = false; renderModel(c);
    });
  }
  const addCustom = sec.querySelector("#m-add-custom");
  if (addCustom) {
    addCustom.addEventListener("click", () => {
      modelEditing = null; addingPicker = false; addingKeyId = null; customAdding = true; renderModel(c);
    });
  }

  // Key-only setup save (dsh addCard): POST /api/config/provider {id, api_key}.
  const addKeySave = sec.querySelector("#m-addkey-save");
  if (addKeySave) {
    const cancel = sec.querySelector("#m-model-cancel");
    const status = sec.querySelector("#m-model-status");
    cancel.addEventListener("click", () => { addingKeyId = null; renderModel(c); });
    addKeySave.addEventListener("click", async () => {
      const key = (sec.querySelector("#m-addkey-value").value || "").trim();
      if (!key) { status.textContent = "请输入 API Key（留空没有意义）"; return; }
      status.textContent = "保存中…";
      addKeySave.disabled = true;
      try {
        const res = await api("/api/config/provider", { method: "POST", body: JSON.stringify({ id: addingKeyId, api_key: key }) });
        if (res.status === 401) { showLogin("令牌无效或已过期"); return; }
        if (!res.ok) { const eb = await res.json().catch(() => ({})); throw new Error(eb.error || ("HTTP " + res.status)); }
        status.textContent = "已保存并生效 ✓";
        addingKeyId = null;
        await loadConfig();
        renderModel(config);
      } catch (e) {
        status.textContent = "失败：" + e.message;
      } finally { addKeySave.disabled = false; }
    });
    return;
  }

  // Wire the editor / custom-create actions.
  const apply = sec.querySelector("#m-model-apply");
  if (!apply) return;
  const cancel = sec.querySelector("#m-model-cancel");
  const status = sec.querySelector("#m-model-status");
  cancel.addEventListener("click", () => { modelEditing = null; customAdding = false; addingPicker = false; addingKeyId = null; renderModel(c); });

  if (customAdding) {
    // 增加自定义提供方: POST /api/config/provider {custom:true, ...}
    apply.addEventListener("click", async () => {
      const route = (sec.querySelector("#m-custom-route").value || "").trim();
      const name = (sec.querySelector("#m-custom-name").value || "").trim();
      const base = (sec.querySelector("#m-custom-base").value || "").trim();
      const model = (sec.querySelector("#m-custom-model").value || "").trim();
      const key = (sec.querySelector("#m-custom-key").value || "").trim();
      if (!route || !name || !base || !model) { status.textContent = "路由 / 名称 / API 地址 / 模型 均必填"; return; }
      status.textContent = "保存中…";
      apply.disabled = true;
      try {
        const res = await api("/api/config/provider", { method: "POST", body: JSON.stringify({
          id: route, name, base_url: base, model, api_key: key, custom: true,
        }) });
        if (res.status === 401) { showLogin("令牌无效或已过期"); return; }
        if (!res.ok) { const eb = await res.json().catch(() => ({})); throw new Error(eb.error || ("HTTP " + res.status)); }
        status.textContent = "已保存 ✓";
        modelEditing = null; customAdding = false;
        await loadConfig();
        renderModel(config);
      } catch (e) {
        status.textContent = "失败：" + e.message;
      } finally { apply.disabled = false; }
    });
    return;
  }

  const editId = modelEditing;
  const target = sorted.find((x) => x.id === editId);
  const active = editId === currentProvider;
  const input = sec.querySelector("#m-model-name");
  const keyInput = sec.querySelector("#m-provider-key");
  apply.addEventListener("click", async () => {
    const body = {};
    if (!active) body.provider = editId;
    const m = input.value.trim();
    if (m && m !== (active ? currentModel : (target && target.model))) body.model = m;
    const key = keyInput.value.trim();
    if (key) body.api_key = key;
    if (!body.provider && !body.model && !body.api_key) { status.textContent = "未发生变化"; return; }
    status.textContent = "应用中…";
    apply.disabled = true;
    try {
      // Key override (M11) is persisted via the provider API first, then the
      // model switch applies live (P5.1).
      if (body.api_key) {
        const rk = await api("/api/config/provider", { method: "POST", body: JSON.stringify({ id: editId, api_key: key }) });
        if (rk.status === 401) { showLogin("令牌无效或已过期"); return; }
        if (!rk.ok) { const eb = await rk.json().catch(() => ({})); throw new Error(eb.error || ("HTTP " + rk.status)); }
      }
      if (body.provider || body.model) {
        const rm = await api("/api/config/model", { method: "POST", body: JSON.stringify(body) });
        if (rm.status === 401) { showLogin("令牌无效或已过期"); return; }
        if (!rm.ok) { const eb = await rm.json().catch(() => ({})); throw new Error(eb.error || ("HTTP " + rm.status)); }
      }
      status.textContent = "已生效 ✓";
      await loadConfig();        // refresh the config view (model/provider)
      loadConfigLabels();        // update the sidebar mode/model badge
      renderModel(config);       // re-render with the new selection
    } catch (e) {
      status.textContent = "失败：" + e.message;
    } finally {
      apply.disabled = false;
    }
  });
}

function renderCaps(c) {
  const rows = [];
  for (const k of Object.keys(c)) {
    if (!k.endsWith("_enabled") || typeof c[k] !== "boolean") continue;
    const short = k.replace(/_enabled$/, "");
    rows.push([CAPABILITY_NAMES[short] || short, k, c[k]]);
  }
  rows.sort((a, b) => a[0].localeCompare(b[0]));
  const sec = settingsSectionEl();
  sec.innerHTML = `<h2>能力开关</h2>
    <p class="intro">各能力默认关闭（D10），启用需在 config.yaml 打开对应开关。</p>
    <p class="notice">修改 config.yaml 后重启生效（无运行时热改）。</p>`;
  for (const [name, key, on] of rows) {
    const d = document.createElement("div");
    d.innerHTML = rowHTML(name, `config 键：${esc(key)}`, `<span class="cap-badge ${on ? "on" : ""}">${on ? "开" : "关"}</span>`);
    sec.appendChild(d.firstElementChild);
  }
}

function renderTools(c) {
  const list = Array.isArray(c.tools_enabled) ? c.tools_enabled : [];
  const sec = settingsSectionEl();
  let t = `<h2>工具白名单</h2>
    <p class="intro">当前白名单放行的工具（只读列表）。</p>
    <p class="notice">修改 config.yaml 后重启生效（无运行时热改）。</p>
    <div class="tools-count">已启用 ${esc(String(c.tools_enabled_count ?? list.length))} 个工具</div>`;
  if (list.length > 0) {
    t += `<div class="tool-list">`;
    for (const tool of list) t += `<div class="tool-row"><span class="tool-dot"></span>${esc(tool)}</div>`;
    t += `</div>`;
  } else {
    t += `<div class="muted" style="margin-top:6px">白名单为空（仅内置只读工具可用）</div>`;
  }
  sec.innerHTML = t;
}

function renderSettingsSec() {
  const c = settingsConfig;
  const sec = settingsSectionEl();
  sec.textContent = "";
  $("settings-sec-title").textContent = SETTINGS_SECTIONS.find((s) => s.id === settingsSec)?.label || "";
  if (!c) { sec.innerHTML = `<div class="muted">加载中…</div>`; return; }
  if (settingsSec === "general") renderGeneral(c);
  else if (settingsSec === "model") renderModel(c);
  else if (settingsSec === "caps") renderCaps(c);
  else if (settingsSec === "tools") renderTools(c);
}

async function renderSettings() {
  const loading = $("settings-loading"), errEl = $("settings-error");
  loading.classList.remove("hidden");
  errEl.classList.add("hidden");
  try {
    const res = await api("/api/config");
    settingsConfig = await res.json();
    config = settingsConfig;
    loadConfigLabels();
    loading.classList.add("hidden");
    renderSettingsNav();
    renderSettingsSec();
  } catch (e) {
    loading.classList.add("hidden");
    errEl.textContent = "加载设置失败：" + e.message;
    errEl.classList.remove("hidden");
  }
}

// ---- routing --------------------------------------------------------------------
async function route() {
  const h = location.hash;
  workspaceEl.classList.toggle("hidden", !(h === "" || h === "#/" || h.startsWith("#/chat")));
  settingsEl.classList.toggle("hidden", h !== "#/settings");
  placeholderEl.classList.toggle("hidden", h !== "#/kb" && h !== "#/kb/");
  if (h === "#/settings") { renderSettings(); }
  else if (h === "#/kb" || h === "#/kb/") {
    $("ph-title").textContent = "知识库";
    $("ph-note").textContent = "KB 全量后挂（占位）。";
  }
}
window.addEventListener("hashchange", () => route());

// ---- P4: runs panel (subagents + background jobs, dsh ui-subagent / ui-jobs)
// The sidebar shortcut was removed on user request; the panel logic stays for
// a future entry (runs-tab is gone, so the panel is currently unreachable).
// ----------------------------------------------------------------------------
const runsPanel = $("runs-panel"), runsTab = $("runs-tab"),
      runsSubs = $("runs-subs"), runsJobs = $("runs-jobs"), runsRefresh = $("runs-refresh");
let runsOpen = false, runsPollTimer = null, runsClockTimer = null, runsBusy = false;

const JOB_STATUS_WORDS = {
  running: "运行中", stopping: "正在停止", completed: "已完成", killed: "已取消", failed: "已失败",
};
function jobDotState(status) {
  if (status === "running") return "running";
  if (status === "stopping" || status === "killed") return "warning";
  if (status === "failed") return "error";
  return "done"; // completed / unknown
}
function fmtDuration(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
  if (h > 0) return `${h}小时${m}分`;
  if (m > 0) return `${m}分${sec}秒`;
  return `${sec}秒`;
}

function toggleRuns(force) {
  const next = force !== undefined ? force : !runsOpen;
  if (next === runsOpen) return;
  runsOpen = next;
  runsPanel.classList.toggle("hidden", !runsOpen);
  if (runsTab) runsTab.classList.toggle("active", runsOpen);
  if (runsOpen) {
    loadRuns();
    startRunsTimers();
  } else {
    stopRunsTimers();
  }
}
function startRunsTimers() {
  stopRunsTimers();
  // 1s live duration clock (only matters while a job is live; cheap enough)
  runsClockTimer = setInterval(() => {
    document.querySelectorAll(".run-duration[data-live]").forEach((el) => {
      const start = Number(el.dataset.start);
      if (start) el.textContent = fmtDuration(Date.now() - start);
    });
  }, 1000);
  // 10s list refresh; paused while the tab is hidden
  runsPollTimer = setInterval(() => {
    if (document.visibilityState !== "hidden") loadRuns();
  }, 10000);
}
function stopRunsTimers() {
  if (runsClockTimer) { clearInterval(runsClockTimer); runsClockTimer = null; }
  if (runsPollTimer) { clearInterval(runsPollTimer); runsPollTimer = null; }
}

// orderedJobs mirrors dsh ui-jobs ordered(): live (running/stopping) first by
// startedAt ascending, then settled by finishedAt descending.
function orderedJobs(jobs) {
  const live = [], settled = [];
  for (const j of jobs) {
    if (j.status === "running" || j.status === "stopping") live.push(j);
    else settled.push(j);
  }
  live.sort((a, b) => new Date(a.started_at) - new Date(b.started_at));
  settled.sort((a, b) => new Date(b.finished_at) - new Date(a.finished_at));
  return live.concat(settled);
}

function renderSubagents(list) {
  if (!Array.isArray(list)) return;
  runsSubs.textContent = "";
  if (list.length === 0) {
    runsSubs.innerHTML = `<div class="runs-empty">暂无子代理</div>`;
    return;
  }
  const rows = [...list].sort((a, b) => (b.running ? 1 : 0) - (a.running ? 1 : 0));
  for (const s of rows) {
    const row = document.createElement("div");
    row.className = "run-row";
    const state = s.running ? "running" : "done";
    row.innerHTML = `
      <span class="p4-dot" data-state="${state}"></span>
      <span class="run-label" title="${esc(s.id || "")}">${esc(s.label || s.id || "")}</span>
      <span class="run-sub">${s.running ? "正在运行" : "当前未运行"}</span>`;
    runsSubs.appendChild(row);
  }
}

function renderJobs(list) {
  if (!Array.isArray(list)) return;
  runsJobs.textContent = "";
  if (list.length === 0) {
    runsJobs.innerHTML = `<div class="runs-empty">暂无后台任务</div>`;
    return;
  }
  for (const j of orderedJobs(list)) {
    const isLive = j.status === "running" || j.status === "stopping";
    const start = new Date(j.started_at).getTime();
    const dur = isLive
      ? (start ? fmtDuration(Date.now() - start) : "")
      : (j.finished_at ? fmtDuration(new Date(j.finished_at) - start) : "");
    const row = document.createElement("div");
    row.className = "run-row" + (isLive ? "" : " settled");
    row.innerHTML = `
      <span class="p4-dot" data-state="${jobDotState(j.status)}"></span>
      ${j.kind ? `<span class="run-kind">${esc(j.kind)}</span>` : ""}
      <span class="run-label" title="${esc(j.label || j.id || "")}">${esc(j.label || j.id || "")}</span>
      <span class="run-sub" title="${esc(j.detail || "")}">${esc(j.detail || JOB_STATUS_WORDS[j.status] || j.status)}</span>
      <span class="run-duration"${isLive && start ? ` data-live data-start="${start}"` : ""}>${dur}</span>`;
    runsJobs.appendChild(row);
  }
}

async function loadRuns() {
  if (runsBusy) return;
  runsBusy = true;
  runsRefresh.classList.add("spinning");
  // Show loading placeholders on the first open only.
  if (!runsSubs.dataset.loaded) { runsSubs.innerHTML = `<div class="runs-loading">正在加载子代理…</div>`; runsJobs.innerHTML = `<div class="runs-loading">正在加载任务…</div>`; }
  try {
    const [subsRes, jobsRes] = await Promise.all([api("/api/subagents"), api("/api/jobs")]);
    const subs = subsRes.status === 501 ? [] : await subsRes.json();
    const jobs = jobsRes.status === 501 ? [] : await jobsRes.json();
    runsSubs.dataset.loaded = "1"; runsJobs.dataset.loaded = "1";
    renderSubagents(subs);
    renderJobs(jobs);
  } catch (e) {
    if (e.message === "unauthorized") { toggleRuns(false); }
    const msg = e.message || "未知错误";
    if (!runsSubs.dataset.loaded) {
      runsSubs.innerHTML = `<div class="runs-error">加载失败：${esc(msg)}<button class="runs-retry">重试</button></div>`;
      runsSubs.querySelector(".runs-retry").addEventListener("click", () => loadRuns());
    }
    if (!runsJobs.dataset.loaded) {
      runsJobs.innerHTML = `<div class="runs-error">加载失败：${esc(msg)}<button class="runs-retry">重试</button></div>`;
      runsJobs.querySelector(".runs-retry").addEventListener("click", () => loadRuns());
    }
  } finally {
    runsBusy = false;
    runsRefresh.classList.remove("spinning");
  }
}

if (runsTab) runsTab.addEventListener("click", (e) => { e.stopPropagation(); toggleRuns(); });
runsRefresh.addEventListener("click", (e) => { e.stopPropagation(); loadRuns(); });
runsPanel.addEventListener("click", (e) => e.stopPropagation());
document.addEventListener("click", (e) => { if (!e.target.closest("#runs-panel, #runs-tab")) toggleRuns(false); });document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "hidden") stopRunsTimers();
  else if (runsOpen) startRunsTimers();
});

// ---- boot ------------------------------------------------------------------------
function boot() {
  injectIcons();
  hideLogin();
  workspaceEl.classList.remove("hidden");
  setupDrag();
  setupNarrow();
  renderColumns();
  syncSidebarToggle();
  initThemeSystem();
  applyTheme();
  syncGrow();
  updatePlaceholder();
  loadConfig();
  loadSessions();
  if (currentID) openSession(currentID);
  else { heroEl.classList.remove("hidden"); }
  pollTimer = setInterval(() => loadSessions(), 30000);
  route();
}

loginForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const t = $("tok").value.trim();
  if (!t) return;
  localStorage.setItem(KEY_TOKEN, t);
  loginMsg.classList.add("hidden");
  await boot();
});
newSessionBtn.addEventListener("click", () => newSession());
// Brand button: a New-Session shortcut while wide, an expand affordance in the
// rail (dsh SidebarRoot brand). The panel toggle folds/expands the column.
$("brand").addEventListener("click", () => {
  if (sidebarCollapsed()) toggleSidebar();
  else newSession();
});
$("sidebar-toggle").addEventListener("click", toggleSidebar);
$("settings-link").addEventListener("click", () => location.hash = "#/settings");
$("theme-toggle").addEventListener("click", toggleTheme);
$("theme-toggle-settings").addEventListener("click", toggleTheme);
$("settings-back").addEventListener("click", () => location.hash = "#/chat");
$("settings-close").addEventListener("click", () => location.hash = "#/chat");
$("back").addEventListener("click", () => location.hash = "#/chat");

boot();

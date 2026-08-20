/* Personal Agent — dsh-style workspace (P1). Vanilla JS, no build, zero
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
let layout = { sidebar: SIDEBAR_DEFAULT, narrow: false, dragging: false };
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

// ---- theme ---------------------------------------------------------------
function applyTheme() {
  const dark = (localStorage.getItem(KEY_THEME) || "dark") !== "light";
  document.body.setAttribute("data-ds-dark-theme", dark ? "true" : "false");
  const icon = $("theme-toggle");
  if (icon) icon.textContent = dark ? "☀️" : "🌙";
  const icon2 = $("theme-toggle-settings");
  if (icon2) icon2.textContent = dark ? "☀️" : "🌙";
}
function toggleTheme() {
  const dark = (localStorage.getItem(KEY_THEME) || "dark") === "dark";
  localStorage.setItem(KEY_THEME, dark ? "light" : "dark");
  applyTheme();
}

// ---- layout: frame grid + drag + narrow -----------------------------------
function renderColumns() {
  frameEl.style.gridTemplateColumns =
    (layout.narrow ? SIDEBAR_COLLAPSED : layout.sidebar) + "px minmax(0, 1fr) 0px";
  frameEl.dataset.sidebarCollapsed = String(layout.narrow);
  frameEl.dataset.detailsCollapsed = "true";
}
function clampSidebar(v) { return Math.max(SIDEBAR_MIN, Math.min(SIDEBAR_MAX, v)); }

function setupDrag() {
  const handle = document.createElement("div");
  handle.className = "drag-handle";
  handle.dataset.side = "sidebar";
  frameEl.appendChild(handle);
  handle.style.left = (layout.narrow ? SIDEBAR_COLLAPSED : layout.sidebar) + "px";

  let origin = 0, base = layout.sidebar, frame = null;
  handle.addEventListener("pointerdown", (e) => {
    if (layout.narrow) return; // no handle while collapsed
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
      handle.style.left = layout.sidebar + "px";
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
    layout.narrow = w < SIDEBAR_AUTO_COLLAPSE;
    renderColumns();
    const h = document.querySelector(".drag-handle");
    if (h) h.style.left = (layout.narrow ? SIDEBAR_COLLAPSED : layout.sidebar) + "px";
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
function addUserMsg(text, timeIso) {
  const inner = msgInner();
  const node = document.createElement("div");
  node.className = "msg user";
  node.innerHTML = `<div class="msg-time">${fmtTime(timeIso)}</div><div class="bubble">${esc(text)}</div>`;
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

function addAssistant(text, timeIso, copyable) {
  const inner = msgInner();
  const node = document.createElement("div");
  node.className = "msg assistant";
  node.innerHTML = `
    <div class="msg-time">${fmtTime(timeIso)}</div>
    <div class="markdown">${text ? renderMarkdown(text) : "<p></p>"}</div>
    <button class="copy-btn" title="复制">复制</button>`;
  const btn = node.querySelector(".copy-btn");
  if (btn && copyable) {
    btn.addEventListener("click", () => {
      navigator.clipboard?.writeText(text).catch(() => {});
    });
  } else if (btn) { btn.remove(); }
  inner.appendChild(node);
  return node.querySelector(".markdown");
}

// appendAssistantStreaming: mutate the live assistant bubble with chunk text.
function appendAssistantStreaming(chunk) {
  let md = streamState && streamState.node;
  if (!md) {
    removeRunning();
    const node = addAssistant("", null, true);
    streamState = { node };
  }
  streamState.node.append(esc(chunk));
  scrollToBottom(true);
}
function finishAssistant(text, timeIso) {
  removeRunning();
  if (streamState && streamState.node) {
    // replace accumulated DOM text with the final rendered markdown
    streamState.node.innerHTML = text ? renderMarkdown(text) : "<p></p>";
    streamState = null;
  } else if (text) {
    // replay path (snapshot with no streaming chunks): render the bubble fresh
    addAssistant(text, timeIso, true);
  }
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

// ---- session list -----------------------------------------------------------
async function loadSessions() {
  let res;
  try {
    res = await api("/api/sessions");
  } catch (e) { if (e.message !== "unauthorized") console.error(e); return; }
  const list = await res.json();
  sessionList.textContent = "";
  if (!Array.isArray(list) || list.length === 0) {
    const li = document.createElement("li");
    li.className = "session-item";
    li.innerHTML = `<span class="si-title empty">还没有会话，点「＋ 新建」开始</span>`;
    sessionList.appendChild(li);
    return;
  }
  list.sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at));
  for (const s of list) {
    const li = document.createElement("li");
    li.className = "session-item" + (s.id === currentID ? " active" : "");
    li.dataset.id = s.id;
    li.innerHTML = `
      <span class="si-title${s.blank ? " empty" : ""}">${esc(s.title || s.id)}</span>
      <span class="si-meta">${fmtTime(s.updated_at)}${s.blank ? " · 空会话" : ""}</span>`;
    li.addEventListener("click", () => switchSession(s.id));
    sessionList.appendChild(li);
  }
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
    case "user/message": addUserMsg(ev.summary || "", ev.time); break;
    case "assistant/message":
      if (ev.reasoning) addReasoning(ev.reasoning, ev.time);
      finishAssistant(ev.summary || "", ev.time);
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
    appendAssistantStreaming(ev.summary || "");
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
  if (!text || !currentID) return;
  setComposerDisabled(true);
  try {
    addUserMsg(text, new Date().toISOString());
    addRunning();
    composerText.value = "";
    syncGrow();
    const res = await api(`/api/sessions/${encodeURIComponent(currentID)}/message`, {
      method: "POST",
      body: JSON.stringify({ text }),
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
composerText.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    sendMessage();
  }
});
sendBtn.addEventListener("click", sendMessage);

// ---- topbar / config ----------------------------------------------------------
async function loadConfig() {
  try {
    const res = await api("/api/config");
    config = await res.json();
    modelLabelEl.textContent = (config.model || "") + (config.llm_provider ? " · " + config.llm_provider : "");
    if (config.mode) {
      modeBadgeEl.textContent = config.mode;
      modeBadgeEl.classList.remove("hidden", "mode-minimal", "mode-code");
      if (config.mode === "minimal") modeBadgeEl.classList.add("mode-minimal");
      if (config.mode === "code") modeBadgeEl.classList.add("mode-code");
    }
  } catch (e) { if (e.message !== "unauthorized") console.error(e); }
}

// ---- settings page (read-only config view) ------------------------------------
function settingsGroup(title, rows) {
  const g = document.createElement("div");
  g.className = "settings-group";
  let t = `<h3>${esc(title)}</h3><table>`;
  for (const [k, v] of rows) {
    const vs = v === true ? "✓" : v === false ? "✗" : String(v ?? "");
    t += `<tr><td>${esc(k)}</td><td class="val">${esc(vs)}</td></tr>`;
  }
  g.innerHTML = t + "</table>";
  return g;
}
async function renderSettings() {
  const groups = $("settings-groups"), loading = $("settings-loading"), errEl = $("settings-error");
  loading.classList.remove("hidden");
  errEl.classList.add("hidden");
  groups.classList.add("hidden");
  try {
    const res = await api("/api/config");
    const c = await res.json();
    groups.textContent = "";
    const basics = [["模型", c.model], ["Provider", c.llm_provider], ["模式", c.mode], ["Base URL", c.base_url]];
    groups.appendChild(settingsGroup("基本", basics));
    const caps = [];
    for (const k of Object.keys(c)) {
      if (k.endsWith("_enabled")) caps.push([k.replace(/_enabled$/, ""), c[k]]);
    }
    caps.sort((a, b) => a[0].localeCompare(b[0]));
    groups.appendChild(settingsGroup("能力开关（默认关闭）", caps));
    if (c.web_server_addr) groups.appendChild(settingsGroup("Web", [["地址", c.web_server_addr]]));
    if (Array.isArray(c.tools_enabled)) {
      groups.appendChild(settingsGroup(`工具白名单（${c.tools_enabled_count} 个）`,
        c.tools_enabled.map((t) => [t, ""])));
    }
    loading.classList.add("hidden");
    groups.classList.remove("hidden");
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

// ---- boot ------------------------------------------------------------------------
function boot() {
  hideLogin();
  workspaceEl.classList.remove("hidden");
  setupDrag();
  setupNarrow();
  renderColumns();
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
$("settings-link").addEventListener("click", () => location.hash = "#/settings");
$("theme-toggle").addEventListener("click", toggleTheme);
$("theme-toggle-settings").addEventListener("click", toggleTheme);
$("settings-back").addEventListener("click", () => location.hash = "#/chat");
$("back").addEventListener("click", () => location.hash = "#/chat");

boot();

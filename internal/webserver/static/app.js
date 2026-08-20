// Personal Agent — dsh-style chat workspace (M10 W1, ADR D-WEB2-F). Vanilla
// JS, no build step, zero dependencies. The bearer token lives in localStorage
// and rides every fetch/SSE request in the Authorization header ONLY (never the
// URL — EventSource cannot send headers, so the stream is consumed with fetch +
// ReadableStream, ADR D-WEB2-B). The chat view is the single main page; the
// session list (new/resume/switch) sits in the left sidebar. #/chat/{id} is the
// route; legacy read-only routes redirect here while their APIs stay for
// internal use.

"use strict";

const KEY_TOKEN = "pa_token";
const KEY_CURRENT = "pa_current_id";
const KEY_THEME = "pa_theme";
const SSE_RETRY_MS = 3000;

const loginEl = document.getElementById("login");
const loginForm = document.getElementById("login-form");
const loginMsg = document.getElementById("login-msg");
const tokInput = document.getElementById("tok");
const workspaceEl = document.getElementById("workspace");
const sessionListEl = document.getElementById("session-list");
const newSessionBtn = document.getElementById("new-session");
const curSessionEl = document.getElementById("cur-session");
const modelLabelEl = document.getElementById("model-label");
const themeBtn = document.getElementById("theme-toggle");
const messagesEl = document.getElementById("messages");
const inputEl = document.getElementById("input");
const sendBtn = document.getElementById("send");
const placeholderEl = document.getElementById("placeholder");
const phTitle = document.getElementById("ph-title");
const phNote = document.getElementById("ph-note");
const backBtn = document.getElementById("back");

let currentID = localStorage.getItem(KEY_CURRENT) || "";
let activeStreamID = ""; // the session the open SSE stream belongs to
let sseSeq = 0;          // bumped on every connect/stop to invalidate stale streams
let sseAbort = null;     // AbortController of the open stream
let lastSeq = 0;         // highest seq rendered for the active stream
let sending = false;

// ---- token / api ---------------------------------------------------------

function token() { return localStorage.getItem(KEY_TOKEN) || ""; }

// api performs an authenticated JSON request; a 401 drops to the login view.
async function api(path, opts) {
  const res = await fetch(path, {
    method: (opts && opts.method) || "GET",
    headers: {
      Authorization: "Bearer " + token(),
      ...(opts && opts.body ? { "Content-Type": "application/json" } : {}),
    },
    body: opts && opts.body,
  });
  if (res.status === 401) {
    showLogin("令牌无效或已过期，请重新输入");
    throw new Error("unauthorized");
  }
  if (!res.ok) {
    let msg = "HTTP " + res.status;
    try { msg = (await res.json()).error || msg; } catch (_) { /* keep msg */ }
    throw new Error(msg);
  }
  return res.json();
}

// ---- views ---------------------------------------------------------------

function showLogin(msg) {
  stopSSE();
  currentID = "";
  activeStreamID = "";
  loginMsg.textContent = msg || "";
  loginMsg.classList.toggle("hidden", !msg);
  loginEl.classList.remove("hidden");
  workspaceEl.classList.add("hidden");
  placeholderEl.classList.add("hidden");
  tokInput.focus();
}

function showWorkspace() {
  loginEl.classList.add("hidden");
  workspaceEl.classList.remove("hidden");
  placeholderEl.classList.add("hidden");
}

function showPlaceholder(title, note) {
  stopSSE();
  activeStreamID = "";
  loginEl.classList.add("hidden");
  workspaceEl.classList.add("hidden");
  phTitle.textContent = title;
  phNote.textContent = note;
  placeholderEl.classList.remove("hidden");
}

// ---- routing -------------------------------------------------------------

async function route() {
  const parts = location.hash.replace(/^#\/?/, "").split("/");
  const view = parts[0] || "chat";
  if (view === "chat") {
    if (parts[1]) {
      await openChat(decodeURIComponent(parts[1]));
    } else {
      // No id → the most recent session, or a fresh one.
      try {
        const list = await api("/api/sessions");
        if (list && list.length) {
          location.hash = "#/chat/" + encodeURIComponent(list[0].id);
          return;
        }
      } catch (e) { /* fall through to a new session */ }
      await newSession();
    }
  } else if (view === "settings") {
    showPlaceholder("设置", "配置页在 W2 提供只读脱敏展示。当前：修改 config.yaml 后重启生效。");
  } else if (view === "kb") {
    showPlaceholder("知识库", "KB 管理台待 KB 全量后挂。");
  } else {
    // Unknown / legacy read-only routes redirect to the chat workspace.
    location.hash = "#/chat";
  }
}

// ---- session management --------------------------------------------------

function shortID(id) { return id.length > 18 ? id.slice(0, 18) + "…" : id; }

function fmtTime(t) {
  if (!t) return "";
  const d = new Date(t);
  if (isNaN(d.getTime())) return "";
  return d.toLocaleString("zh-CN", {
    month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false,
  });
}

async function loadSessions() {
  const list = await api("/api/sessions");
  sessionListEl.innerHTML = "";
  if (!list || !list.length) {
    const li = document.createElement("li");
    li.className = "session-empty muted";
    li.textContent = "暂无会话";
    sessionListEl.appendChild(li);
    return;
  }
  for (const s of list) {
    const li = document.createElement("li");
    li.className = "session-item" + (s.id === currentID ? " active" : "");
    const a = document.createElement("a");
    a.href = "#/chat/" + encodeURIComponent(s.id);
    a.title = s.id + " · " + s.event_count + " 事件";
    const name = document.createElement("div");
    name.className = "session-name";
    name.textContent = shortID(s.id);
    const meta = document.createElement("div");
    meta.className = "session-meta";
    meta.textContent = fmtTime(s.updated_at);
    a.appendChild(name);
    a.appendChild(meta);
    li.appendChild(a);
    sessionListEl.appendChild(li);
  }
}

async function newSession() {
  const res = await api("/api/sessions", { method: "POST" });
  location.hash = "#/chat/" + encodeURIComponent(res.id);
}

async function openChat(id) {
  currentID = id;
  activeStreamID = id;
  localStorage.setItem(KEY_CURRENT, id);
  showWorkspace();
  curSessionEl.textContent = "会话 " + id;
  // The model/provider label is populated by W2's config API; until then it
  // honestly shows a placeholder.
  modelLabelEl.textContent = "model —";
  try {
    await loadSessions();
  } catch (e) {
    if (e.message === "unauthorized") return;
  }
  messagesEl.innerHTML = "";
  lastSeq = 0;
  connectSSE(id);
  inputEl.focus();
}

// ---- message rendering ---------------------------------------------------

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, c =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function scrollBottom() {
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

function addUserBubble(text, optimistic) {
  const msg = document.createElement("div");
  msg.className = "msg user" + (optimistic ? " optimistic" : "");
  const bubble = document.createElement("div");
  bubble.className = "bubble";
  bubble.textContent = text;
  msg.appendChild(bubble);
  messagesEl.appendChild(msg);
  scrollBottom();
  return msg;
}

function addAssistantBubble() {
  const msg = document.createElement("div");
  msg.className = "msg assistant streaming";
  const bubble = document.createElement("div");
  bubble.className = "bubble";
  msg.appendChild(bubble);
  messagesEl.appendChild(msg);
  scrollBottom();
  return bubble;
}

function currentAssistantBubble() {
  const msgs = messagesEl.querySelectorAll(".msg.assistant.streaming");
  if (!msgs.length) return null;
  return msgs[msgs.length - 1].querySelector(".bubble");
}

function addToolCard(summary, isError) {
  const card = document.createElement("div");
  card.className = "tool-card" + (isError ? " error" : "");
  const head = document.createElement("button");
  head.type = "button";
  head.className = "tool-head";
  head.innerHTML = (isError ? "⚠ " : "🛠 ") + esc(summary);
  const body = document.createElement("div");
  body.className = "tool-body hidden";
  body.textContent = summary;
  head.addEventListener("click", () => body.classList.toggle("hidden"));
  card.appendChild(head);
  card.appendChild(body);
  messagesEl.appendChild(card);
  scrollBottom();
}

function handleEvent(ev) {
  if (!ev || activeStreamID !== currentID) return; // stale / other-session stream
  if (typeof ev.seq === "number" && ev.seq <= lastSeq) return; // dedupe
  if (typeof ev.seq === "number") lastSeq = ev.seq;
  switch (ev.type) {
    case "user/message": {
      // Confirm the optimistic bubble (it is always the newest user bubble);
      // otherwise render the event.
      const opt = messagesEl.querySelector(".msg.user.optimistic:last-child");
      if (opt) {
        opt.classList.remove("optimistic");
      } else {
        addUserBubble(ev.summary || "", false);
      }
      break;
    }
    case "assistant/chunk": {
      let bubble = currentAssistantBubble();
      if (!bubble) bubble = addAssistantBubble();
      bubble.textContent += ev.summary || "";
      scrollBottom();
      break;
    }
    case "assistant/message": {
      const streamed = currentAssistantBubble();
      if (streamed) {
        streamed.textContent = ev.summary || "";
        streamed.parentElement.classList.remove("streaming");
      } else {
        const bubble = addAssistantBubble();
        bubble.textContent = ev.summary || "";
        bubble.parentElement.classList.remove("streaming");
      }
      break;
    }
    case "tool/result":
      addToolCard(ev.summary || "", false);
      break;
    case "tool/error":
      addToolCard(ev.summary || "", true);
      break;
    default:
      // Log-only events (kb/*, job/*, subagent/*, plan/*, …) are not part of
      // the chat surface.
      break;
  }
}

// ---- SSE (fetch + ReadableStream; EventSource cannot send the header) -----

function stopSSE() {
  sseSeq++;
  if (sseAbort) { sseAbort.abort(); sseAbort = null; }
}

function scheduleReconnect(id) {
  setTimeout(() => {
    if (currentID === id && activeStreamID === id) connectSSE(id);
  }, SSE_RETRY_MS);
}

async function connectSSE(id) {
  const mySeq = ++sseSeq;
  const ac = new AbortController();
  sseAbort = ac;
  try {
    const res = await fetch(
      "/api/sessions/" + encodeURIComponent(id) + "/events/stream",
      { headers: { Authorization: "Bearer " + token() }, signal: ac.signal }
    );
    if (!res.ok) {
      if (res.status === 401) { showLogin("令牌无效或已过期"); return; }
      throw new Error("SSE HTTP " + res.status);
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let idx;
      while ((idx = buf.indexOf("\n\n")) >= 0) {
        const frame = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        const dataLine = frame.split("\n").find(l => l.startsWith("data: "));
        if (!dataLine) continue;
        try { handleEvent(JSON.parse(dataLine.slice(6))); } catch (_) { /* skip malformed frame */ }
      }
    }
    // The server closed the stream: reconnect (fresh snapshot) unless the user
    // already switched sessions.
    if (mySeq === sseSeq && currentID === id) scheduleReconnect(id);
  } catch (e) {
    if (e.name === "AbortError") return; // expected on session switch
    if (mySeq === sseSeq && currentID === id) scheduleReconnect(id);
  }
}

// ---- composer ------------------------------------------------------------

async function sendMessage() {
  const text = inputEl.value.trim();
  if (!text || sending) return;
  inputEl.value = "";
  inputEl.style.height = "auto";
  addUserBubble(text, true); // optimistic; confirmed by the SSE user/message
  sending = true;
  sendBtn.disabled = true;
  try {
    await api("/api/sessions/" + encodeURIComponent(currentID) + "/message", {
      method: "POST",
      body: JSON.stringify({ text }),
    });
  } catch (e) {
    if (e.message !== "unauthorized") {
      const opt = messagesEl.querySelector(".msg.user.optimistic:last-child .bubble");
      if (opt) opt.textContent = text + "（发送失败：" + e.message + "）";
    }
  } finally {
    sending = false;
    sendBtn.disabled = false;
    inputEl.focus();
  }
}

function autoGrow() {
  inputEl.style.height = "auto";
  inputEl.style.height = Math.min(inputEl.scrollHeight, 160) + "px";
}

// ---- theme ---------------------------------------------------------------

function applyTheme() {
  const t = localStorage.getItem(KEY_THEME) || "dark";
  document.documentElement.dataset.theme = t;
  themeBtn.textContent = t === "dark" ? "☀️" : "🌙";
}

themeBtn.addEventListener("click", () => {
  const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
  localStorage.setItem(KEY_THEME, next);
  applyTheme();
});

// ---- wiring --------------------------------------------------------------

loginForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const t = tokInput.value.trim();
  if (!t) return;
  localStorage.setItem(KEY_TOKEN, t);
  tokInput.value = "";
  try {
    await api("/api/health");
    render();
  } catch (err) {
    if (err.message !== "unauthorized") showLogin("无法连接服务器：" + err.message);
  }
});

newSessionBtn.addEventListener("click", () => {
  newSession().catch(e => { if (e.message !== "unauthorized") console.error(e); });
});

inputEl.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey) {
    e.preventDefault();
    sendMessage();
  }
});
inputEl.addEventListener("input", autoGrow);
sendBtn.addEventListener("click", sendMessage);
backBtn.addEventListener("click", () => { location.hash = "#/chat"; });
window.addEventListener("hashchange", () => { if (token()) route(); });

async function render() {
  if (!token()) { showLogin(""); return; }
  await route();
}

applyTheme();
render();

# M10 升级 W1 派发：交互核心 + dsh 式聊天工作台前端

> ADR `docs/decisions/2026-08-20-m10-web-workspace.md`（D-WEB2-A~F）。本文件是 **W1** 契约：① cmd/pa `turnMu` 串行 + `runTurn` + `eventHub`；② `internal/webserver` 注入点（消息处理器 / 会话管理器 / 事件源）+ `POST /api/sessions/{id}/message` + `GET /api/sessions/{id}/events/stream`（SSE）+ `POST /api/sessions`（new）+ `POST /api/sessions/{id}/resume`；③ **前端整体重构为 dsh 式聊天工作台（唯一主界面，用户拍板：只读 UI 不需要）**。W2（settings/config API）与 W3（验收收尾）在后续段。

## 纪律
- **loop.go 零改动（D4）**；REPL 与 web 发消息串行（D5，turnMu）；零新依赖、CGO-free、gofmt；只动 internal/webserver、cmd/pa、internal/config（如需）、config.yaml。
- 默认关 D10（web_server.enabled=false 不监听、message/SSE 不注册）；token 空 fail-closed。
- 认证：全部 `/api/*`（含 message、SSE、sessions 管理）走既有 `requireAuth`；静态 shell 公开（`6c18446` 后）。**token 只在 Authorization header**，SSE 前端用 fetch 流解析（不放 URL）。
- 提交 1 个：`W1: Web 聊天工作台（发消息 + SSE 流式 + 会话管理 + dsh 式前端重构）`

## 现状（实施时通读）
- M10a/c/b 已交付（`9592406`/`6cd21b9`/`df3992f` + 认证修复 `6c18446`）：`internal/webserver/webserver.go`（`New(store, token, addr)` + `requireAuth` + `Handler()` + `/api/sessions` + `/api/sessions/{id}/events` + `/api/stats` + `/api/kb` 501 + `/api/health`）+ `static/{index.html,app.js,style.css}`（登录 + 会话列表 + 事件流 + dashboard/kb 占位）+ webserver_test.go（10 用例）+ config `web_server` + cmd/pa `registerWebServer`（goroutine Serve + defer Close）+ printHelp。
- cmd/pa/main.go：`attachSink`（514-519：`a.log.SetSink(func(ev) { return a.store.AppendEvents(ctx, id, []session.Event{ev}) })`，session 切换时调用）、`newLoop()`（537-555：`loop.Config{... OnText: func(delta){fmt.Print(delta)}, OnError: ...}`）、`repl`（558-：`scanner.Scan()` → 579 `a.newLoop().Run(ctx, line)`）、`newSession`/`resumeSession`（507-509 区域）、`currentID`、`store`、`log`、`cfg`。
- `internal/loop`：`Loop.Run(ctx, text)`（每 chunk 落 `assistant/chunk`，loop.go:240）；`onText`/`onError` 回调（237/229）。**不改**。
- `internal/session`：`Event{Seq, Type, At, Version, Data}`；`SetSink` 是 Log 的唯一外部事件钩子（每个 Append 后调用）。
- store：`ListSessions`/`LoadSession`/`CreateSession`。

## 变更清单（精确）

### 1. cmd/pa/main.go — turnMu + runTurn + REPL 改用
- app 结构加 `turnMu sync.Mutex`（并发注解：REPL 与 web 发消息共用，任何时刻至多一个 Run，D5）。
- 新方法：
```go
// runTurn executes one turn under the global serial lock (D5: REPL and web
// share one loop; at most one Run at a time). interactive=false suppresses the
// stdout stream (web renders from the SSE event stream instead — chunk 已落库).
func (a *app) runTurn(ctx context.Context, text string, interactive bool) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	lp := a.newLoop()
	if !interactive {
		lp = a.newLoopWeb() // 见下: OnText 静默
	}
	return lp.Run(ctx, text)
}
```
- `newLoopWeb()`：同 newLoop 但 `OnText: func(string) {}`（静默；SSE 事件流负责回显）、`OnError: func(error) {}`（或 stderr 保留——决策：静默，错误经 SSE tool/error / assistant 落库体现；SSE 端也可推 stream error 事件——实施者按简单：OnError 静默）。
  - 更简洁：`newLoop()` 抽一个 `buildLoop(onText, onError func(string), func(error))`，REPL 传 print，web 传 no-op。实施者任选，保持 REPL 现行为不变。
- `repl` 579 行 `a.newLoop().Run(ctx, line)` → `a.runTurn(ctx, line, true)`（REPL 流式到 stdout 行为不变）。
- `attachSink`（514-519）：sink 里加 `a.hub.Publish(ev)`（见 3），保持 store.AppendEvents 返回语义（sink 错误仍要阻断？——Publish 不返回错误，忽略；store 错误仍返回）。

### 2. cmd/pa/webserver.go — eventHub + 注入接线
- 新 `eventHub`（cmd/pa 内）：`type eventHub struct { mu sync.Mutex; subs map[string]map[chan session.Event]struct{} }`；方法：
  - `Publish(ev session.Event)`：广播给该 sessionID 的所有订阅者 chan（非阻塞：chan 缓冲 256，满则丢订阅者——用 select default，防慢订阅者阻塞 loop 持久化路径；诚实：极端下 SSE 丢事件，前端以快照+后续为准）。
  - `Subscribe(sessionID string) (chan session.Event, func())`：返回 chan + 退订闭包。
  - `NewEventHub() *eventHub`。
- app 字段 `hub *eventHub`（NewEventHub 初始化于 app 构造或 registerWebServer）。
- `registerWebServer()`（现有）：在 `webserver.New` 后注入：
  - `srv.SetMessageHandler(func(ctx, sessionID, text string) error { return a.webMessage(ctx, sessionID, text) })`
  - `srv.SetSessionManager(func(ctx, action, id string) (string, error) { ... new/resume ... })`
  - `srv.SetEventSource(func(sessionID string, sink func(session.Event)) func() { return a.hub.SubscribeInto(sessionID, sink) })`（或 Subscribe 后 goroutine 转发——实施者按 hub 设计简单实现）。
- 新方法 `webMessage(ctx, sessionID, text string) error`：`if sessionID != "" && sessionID != a.currentID { a.resumeSession(ctx, sessionID) }`；`return a.runTurn(ctx, text, false)`。空 text → error。
- **注意**：`a.store`/`a.log`/`a.attachSink` 的 session 切换语义——resumeSession 内部已 attachSink 到新会话（507-509 区域），webMessage 直接复用。

### 3. internal/webserver/webserver.go — 注入点 + message/SSE/session 管理 API
- Server 加字段：`msgFn func(ctx context.Context, sessionID, text string) error`、`sessFn func(ctx context.Context, action, id string) (string, error)`、`evSrc func(sessionID string, sink func(session.Event)) func()`（均可 nil，nil → 对应 API 返回 501）。
- Setter：`SetMessageHandler` / `SetSessionManager` / `SetEventSource`（cmd/pa 在 registerWebServer 调用）。
- 新路由（全在 mux，走 requireAuth）：
  - `POST /api/sessions` → `sessFn(ctx, "new", "")` → 200 `{"id": ...}`；msgFn/sessFn nil → 501。
  - `POST /api/sessions/{id}/resume` → `sessFn(ctx, "resume", id)` → 200 `{"id": ...}`；404/错误 → 对应状态。
  - `POST /api/sessions/{id}/message` body `{"text": "..."}` → `msgFn(ctx, id, text)` → 200 `{"ok":true}`（Run 已完成；前端 SSE 已收到过程）。空 text → 400。msgFn nil → 501。
  - `GET /api/sessions/{id}/events/stream` → **SSE**：写 `Content-Type: text/event-stream`；先 `store.LoadSession(id)` 逐事件发 `data: {json}\n\n`（快照）；再 `evSrc(id, sink)` 订阅，`select` 在 chan/context.Done 上；每事件 `data: ...\n\n` + `retry: 3000`；**每帧可加 `id: <seq>`** 供前端断线续。连接关闭/context 取消 → 退订。evSrc nil → 501（或直接 501，不建流）。**注意**：SSE 端点 handler 内不能 writeJSON（需专用 writer，`http.Flusher` 逐帧 Flush）。
- 事件帧 JSON：`{"seq":N,"type":"...","time":"RFC3339","summary":"..."}`——复用现有 `summarize`（eventView 序列化）。
- **保留**既有 `/api/sessions`（列表）、`/api/sessions/{id}/events`、`/api/stats`、`/api/kb` 501、`/api/health`、`/`、`/static/*`（前端重构后这些 API 仍被聊天工作台内部使用）。

### 4. internal/webserver/static/app.js + style.css + index.html — 前端整体重构为 dsh 式聊天工作台（唯一主界面）
- **去掉**独立只读页导航（sessions 列表页 / 事件流浏览页 / dashboard 页作为主页面）；**唯一主界面** = 聊天工作台：
  - 布局：左侧会话栏（宽 ~260px：会话列表 + 「+ 新建」按钮）+ 主区聊天（消息滚动流 + 底部输入框）+ 顶部栏（当前会话 id / 模型 / provider 显示、设置占位入口、主题切换按钮）。
  - 路由：默认 `#/chat/{id}`（无 id → 第一个会话或新建）；历史 `#/chat` 重定向。
  - 消息流渲染：
    - `user/message` → 右侧气泡（文本）。
    - `assistant/message` → 左侧气泡（文本 + 若有 tool_calls 显示调用列表）。渲染「完整消息」用 SSE 快照 + 新事件；流式期间用 `assistant/chunk` 逐字追加到「当前助手气泡」。
    - `assistant/chunk` → 追加到当前未完成的 assistant 气泡（streaming 状态）。
    - `tool/result` / `tool/error` → 工具卡片（折叠：标题 `<name>`，展开显有界 output；error 红色）。工具卡片插入在触发它的 assistant 气泡之后。
  - **SSE 消费**：`fetch('/api/sessions/{id}/events/stream', {headers:{Authorization:'Bearer '+token}})` → `response.body.getReader()` + `TextDecoder` 按 `\n\n` 拆帧解析 `data: {json}`；收到 chunk 更新 DOM；连接中断自动重连（带 `Last-Event-ID`/seq 续传或简单 3s 重连 + 重新快照）。**不要用 EventSource**（无法带 header，ADR D-WEB2-B）。
  - 输入框：Enter 发送 → `POST /api/sessions/{id}/message {text}` → 乐观插入 user 气泡 → 等 SSE。
  - 会话栏：`GET /api/sessions` 渲染列表（当前高亮）；「+ 新建」→ `POST /api/sessions` → 跳 `#/chat/{id}`；点会话 → `POST /api/sessions/{id}/resume` → 跳转。当前会话 id 存内存/localStorage。
  - 主题：深/浅切换（CSS 变量 + localStorage），默认深色（现风格）。
  - 登录视图保留（token 输入存 localStorage）。
  - `#/kb` 占位页保留（KB 空壳）；`#/settings` 占位（W2 填）。
- style.css：网格布局（侧栏+主区）、气泡、工具卡片、输入框、主题变量。可重写，保持零依赖。

### 5. 测试
- `internal/webserver/webserver_test.go` 新增：
  - `TestMessageRequiresAuth`：POST message 无 token → 401。
  - `TestMessageHandlerInvoked`：注入 fake msgFn（记录 (id,text) 返回 nil）→ POST 200 `{"ok":true}` 且参数正确；空 text → 400；未注入（nil）→ 501。
  - `TestSessionNewResume`：注入 fake sessFn → POST /api/sessions → 200 id；POST resume → 200；nil → 501。
  - `TestEventsStreamSSE`：注入 fake evSrc（sink 收到后推一个 event）+ store 预置会话 → GET stream（httptest）→ 响应 `text/event-stream`，body 含快照事件 + 订阅推送事件帧 `data: {...}`；无 evSrc → 501。（httptest 读 SSE：直接读 Body，无需真流式——fake evSrc 同步推一次即可断言。）
- `cmd/pa` 新增/扩展：
  - `runTurn` 串行测试：fake LLM（scripted）+ 并发两 goroutine 调 `runTurn` → 断言执行串行（可用 LLM 调用计数或 lock 时序；实施者用简单计数 + 并发 sleep 验证不并发）。
  - `webMessage` 测试：fake LLM → `webMessage(ctx, currentID, "hi")` → log 有新 user/message + assistant 事件；`sessionID != currentID` → resume 后执行。
  - `eventHub` 测试：Publish → 订阅者收到；慢订阅者不阻塞（缓冲满丢）。
  - `registerWebServer` 注入断言：enabled + token → `a.webserver` 的 msgFn/sessFn/evSrc 非 nil（加 getter 或同包直接断言字段）。
- **保留**既有 10 个 webserver 用例通过（前端重构不影响后端测试）。

## 验证
`go build ./...` + `go vet ./internal/webserver/ ./cmd/pa/` + `go test -count=1 -timeout 90s ./internal/webserver/ ./cmd/pa/ -run 'Message|SessionNew|SessionResume|EventsStream|RunTurn|WebMessage|EventHub|RegisterWebServer|SSE' -v` 全 PASS 后提交；随后 `go test -count=1 -timeout 90s ./...` 全绿确认。env 同 M10a。

## 环境
- Go：`C:\Program Files\Go\bin\go.exe`；env：`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'`；提交身份 `-c user.name='Personal Agent' -c user.email='dev@personal-agent.local'`；pwsh，workdir=`D:\dev-projects\Agent\personal-agent`。
- **警告**：不要用 PowerShell 的 Set-Content/Add-Content 改写含 UTF-8 的文件（会破坏编码 → illegal UTF-8）。改前端/Go 用文件编辑能力写 UTF-8；误改坏就删除重建。
- **尽快产出**：按 1→5 顺序实现，写完核心（1-3）即可先提交一部分再补前端？——**不**：契约要求一次提交。但实现顺序建议 1→2→3（后端+测试可独立验证）→4（前端）→5 测试→提交。

## 报告（简短）
提交 hash + go test 结果 + 偏离说明（hub 丢事件策略、SSE 重连实现、前端布局、注入点实现）。不要贴代码。
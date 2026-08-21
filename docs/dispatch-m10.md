# M10a 派发：webServer 基础设施 + dsh 式会话/事件浏览入口

> M10 Web 门户 ADR `docs/decisions/2026-08-20-m10-web-portal.md`（D-WEB-1~7）。本文件是 **M10a** 契约（统一门户第一段）：`internal/webserver`（net/http 服务 + bearer 认证 + 静态托管 + 会话/事件浏览 API + vanilla JS 前端）+ config + cmd/pa 接线 + 测试。M10c dashboard / M10b KB 管理台空壳在后续段。

## 纪律

- 零新依赖、CGO-free；`net/http` + `crypto/subtle` + `crypto/sha256` + `embed`（全标准库）；只动 internal/webserver、internal/config、cmd/pa、config.yaml；不改 loop（D4）。
- 默认关 D10：`web_server.enabled=false` 不启动监听、不注册任何路由；token 空且 enabled 时启动报错（fail-closed 防裸奔）。
- 只读 API（D-WEB-4）：web 不写会话；事件 API 只传 `{seq,type,time,summary}`，不落完整 Data（防泄露 + 防超大载荷）。
- 认证模型（D-WEB-2）：**静态 shell（`/` 与 `/static/*`）公开**——登录页/前端资源不含数据，必须能在无 token 时加载以呈现登录表单（实测踩坑：若全路由认证，浏览器直开 `http://127.0.0.1:8080` 被 401 JSON 挡住、鸡生蛋，见 `6c18446` 修复）；**全部 `/api/*` 路由走 bearer 认证中间件**，数据只在 API 层流出。
- 提交 1 个：`M10a: webServer 基础设施（统一门户 + bearer 认证 + 会话/事件浏览 API + vanilla JS 前端）`

## 已知现状（实施时通读）

- `internal/session`：`Event{Seq uint64, Type string, Time time.Time, Data json.RawMessage}`（session.go:241）；事件词汇表在 session.go 各 `NewXxx`（user/message、assistant/message、tool/result、tool/error、kb/*、job/*、terminal/*、subagent/*、compaction/*、skill/*、schedule/*、plan/*、spill/*、interact/*、code/run、mcp/*、fs/*、eval/run、ralph/run、workflow/run、web/search-request…）。
- `internal/store`：`Store{ListSessions() ([]SessionMeta, error); LoadSession(id) ([]session.Event, error); …}`；`SessionMeta{ID, CreatedAt, UpdatedAt, EventCount}`。
- `cmd/pa/main.go`：register 序列（registerSubagent 附近 ~154-172 是加 register* 调用处，参考 registerRalph/registerWorkflow 的注释风格）；app 字段（a.cfg/a.store/a.log）；printHelp 状态块（~696 区域，参考 `mode:`/`eval:` 行）。
- `internal/config/config.go`：Config 结构字段 + applyDefaults + minimal 分支（现含 FsSearch/Ralph/Workflow 关闭行）+ config_test 模式。

## 变更清单（精确）

### 1. internal/webserver（新建包）
```go
// Package webserver serves the M10 unified web portal (ADR
// 2026-08-20-m10-web-portal.md): a single net/http server carrying the
// dsh-style session/event browsing entry, later the dashboard and KB admin.
// Read-only (D-WEB-4): it never writes the session log. All API routes sit
// behind the bearer-token middleware; the frontend is vanilla JS embedded
// into the binary (go:embed) — zero new dependencies.
package webserver
```
- **Server** 结构：`{store store.Store, tokenHash [32]byte, addr string, srv *http.Server}`。
- `New(store store.Store, token, addr string) (*Server, error)`：
  - store nil → error；token 空 → error（`fmt.Errorf("webserver: token required (set web_server.token)")`，fail-closed）。
  - `tokenHash = sha256.Sum256([]byte(token))`（明文 token 只在 New 时存在，之后只有摘要）。
  - addr 空 → 默认 `"127.0.0.1:8080"`。
  - 构造 mux（方法+路径，Go 1.22 `ServeMux`）：
    - `GET /` → 静态 index.html（embedded）
    - `GET /static/{file...}` → embedded 静态文件（用 `http.FileServer` over `embed.FS` 子 FS）
    - `GET /api/sessions` → 认证中间件后：`store.ListSessions` → JSON `[{id, created_at, updated_at, event_count}]`
    - `GET /api/sessions/{id}/events` → 认证中间件后：`store.LoadSession(id)` → `ErrNotFound` → `404 {"error":"session not found"}`；否则 JSON `[{seq, type, time, summary}]`（每事件经 `summarize(ev)` 出 summary，见下）
    - `GET /api/health` → `{"ok":true}`（**也走认证**，与其余 API 一致；探活用）
  - 其余路径 → `404`。
- **认证中间件** `requireAuth(next http.Handler)`：读 `Authorization: Bearer <t>`；`sha256(t)` 与 tokenHash 恒时比对（`crypto/subtle.ConstantTimeCompare`）；不符 → `401 {"error":"unauthorized"}`。`/` 与 `/static/*` **不要求认证**（页面本身公开，数据 API 才要 token——前端在 localStorage 存 token 并随 fetch 携带；诚实记录：静态页可被局域网内访问者看到，但无数据）。
  - 等等——ADR D-WEB-2 说"每个请求校验"；为简单与一致，**全部路由（含 / 与 /static）都要求认证**？——取舍：静态页无敏感数据，但个人门户默认全认证更省心。**决策：全路由认证**（含 /、/static），前端登录页（输入 token 存 localStorage）先取 /api/health 验证再加载应用。实施者按此。
- `Handler() http.Handler`（返回 mux，供 httptest）。
- `Serve() error`（`srv.ListenAndServe`，addr 来自 New）；`Close() error`（`srv.Shutdown` 幂等）。
- `summarize(ev session.Event) string`：按 `ev.Type` 提取有界摘要（**不进原始 Data 完整内容**）：
  - `user/message` → 文本（截断 200 runes）；`assistant/message` → 文本/工具调用摘要（截断 200）；`assistant/chunk` → 跳过（summarize 返回 "" 时 events API 的 summary 字段为空串即可，前端忽略）；`tool/result` → `tool <name> → <前 80 runes>`；`tool/error` → 类似；其余类型 → 类型名即可（如 `kb/recall` → "" 或类型名）。实施者实现一个覆盖已知类型的 switch，未知类型返回 ""。截断用 `…` 后缀。
  - 用 `session` 包导出的 data 构造器反解？——不：data 是 `json.RawMessage`，直接 `json.Unmarshal` 到本包私有 struct（按类型只取 text/name 等字段），避免依赖 session 内部 unexported data 类型。实施者看 session.go 各 `NewXxx` 的字段名（如 userMessageData.Text、assistantMessageData.Text、toolResultData{CallID,Name,Output}、…）复制字段定义（JSON 无 tag 的字段名即键名）。

### 2. internal/webserver/static/（前端，go:embed）
- `index.html` + `app.js` + `style.css`（可再拆，保持三文件即可）。vanilla JS 无构建：
  - 登录视图：token 输入 → `fetch('/api/health')` 验证 → 存 localStorage。
  - hash 路由 `#/sessions`（默认）：会话列表（id/created/updated/event_count）→ 点进 `#/sessions/{id}` 事件流（seq/type/time/summary 列表，按类型着色）。
  - `#/dashboard`（本期占位页，M10c 后填）、`#/kb`（占位页"KB 管理台待 KB 全量后挂"）。
  - fetch 统一带 `Authorization: Bearer <localStorage token>`；401 → 回登录视图。
- 样式简洁（个人门户，深色或浅色均可），不引任何外部 CSS/JS。

### 3. internal/webserver/webserver_test.go
- httptest 用例（用内存 fake store 或真实 store + t.TempDir？——**用真实 `store.OpenSQLite(t.TempDir()+"/t.db")`** 造会话+事件，或包内 fake Store 实现。实施者任选，优先 fake 轻量）：
  1. `TestNewRequiresToken`：空 token → error。
  2. `TestAuthRequired`：无/错 Bearer → 401；对 token → 200（/api/sessions）。
  3. `TestSessionsList`：两个会话元数据正确 JSON 返回。
  4. `TestSessionEvents`：事件流返回 seq/type/summary 正确；未知会话 404。
  5. `TestStaticServed`：`/` 返回 index.html（200 + text/html）；`/static/app.js` 200。
  6. `TestSummaryBound`：超长 user/message 文本 summary 被截断（≤200 runes + …）。
  7. `TestHealth`：认证后 200 `{"ok":true}`。

### 4. internal/config/config.go + config.yaml
- `Config.WebServer WebServerConfig`（字段放 Eval 附近或末尾）。
- `WebServerConfig{Enabled bool; Addr string; Token string}`，yaml tags `web_server` 段：
  - `addr` 空 → applyDefaults 填 `"127.0.0.1:8080"`；`token` 空 → 不填（启动时 fail-closed）。
  - **minimal 分支加 `cfg.WebServer.Enabled = false`**（minimal 不含门户）。
- config.yaml 段：
  ```yaml
  # Web 门户 (M10a, ADR 2026-08-20-m10-web-portal.md; 默认关 D10): enabled=false
  # 不启动监听; token 明文只在配置, 进程内只存 SHA-256 摘要; addr 默认本机.
  web_server:
    enabled: false
    addr: 127.0.0.1:8080
    token: ""
  ```
- config_test.go：`TestWebServerDefaults`（addr 默认填、minimal 关闭）。

### 5. cmd/pa/webserver.go + main.go 接线 + printHelp
- `cmd/pa/webserver.go`：`registerWebServer() error`——`!a.cfg.WebServer.Enabled` → nil；`webserver.New(a.store, cfg.Token, cfg.Addr)`（err → fail-closed 启动报错 `os.Exit(1)`）；`go srv.Serve()`（goroutine，错误经 `fmt.Fprintln(os.Stderr)` 报出）；`defer srv.Close()`（注册到 main 生命周期，像 `app.fs.Close()` 的 defer 模式）。server 实例存 `a.webserver` 字段（供测试断言 + printHelp）。
- main.go：register 序列（registerRalph/registerWorkflow 之后或末尾）加 `registerWebServer` 调用 + 注释（D10/ADR 引用/生命周期）；app 结构体加 `webserver *webserver.Server` 字段。
- printHelp 状态块加：`fmt.Printf("web server: enabled (%s) | disabled (web_server.enabled=false)\n", ...)`（或按既有风格，如 `web: disabled` 已有 M7 行——M7 是 web 搜索工具，M10 是 web 门户，**行文要区分**：`web portal: enabled (127.0.0.1:8080)` / `web portal: disabled (web_server.enabled=false)`）。
- `cmd/pa/webserver_test.go`：`TestRegisterWebServerDisabledRegistersNothing`（enabled=false 不启动）+ `TestRegisterWebServerEnabled`（enabled=true + token 非空 → 启动后 `/api/health` 带 token 200）+ 空 token enabled → registerWebServer 返回 error（fail-closed）。测试用 `a.store`（真实 store + t.TempDir）+ httptest 指向 `a.webserver.Handler()` 或直接连接 `a.webserver` 地址（用 `httptest.NewServer(a.webserver.Handler())` 更稳）。

## 验证

`go build ./...` + `go vet ./...` + `go test -count=1 -timeout 90s ./internal/webserver/ ./internal/config/ ./cmd/pa/ -run 'WebServer|Webserver|webserver|Web' -v` 全 PASS 后提交；随后 `go test -count=1 -timeout 90s ./...` 全绿确认。每个 go 命令都设环境（GOCACHE 必须，否则默认目录访问被拒）。

## 环境

- Go：`C:\Program Files\Go\bin\go.exe`；env：`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'`；提交身份 `-c user.name='Personal Agent' -c user.email='dev@shutu-agent.local'`；pwsh，workdir=`D:\dev-projects\Agent\shutu-agent`。

## 报告（简短）
提交 hash + go test 结果 + 偏离说明（summarize 覆盖类型、认证取舍、前端结构）。不要贴代码。

---

# M10c 派发：dashboard 工作台（/api/stats 聚合 + dashboard 页面）

> M10a 已完成（`9592406`）。本段在**已提交的 M10a 之上**追加 dashboard：`/api/stats`（只读聚合）+ 前端 `#/dashboard` 页面。M10b（KB 管理台空壳）在后续段。

## 纪律
- 在 M10a 提交（`9592406`）上工作，工作树干净开始；零新依赖、CGO-free、gofmt；只动 internal/webserver、cmd/pa、internal/config（如需）；不改 loop（D4）。
- `/api/stats` 与既有 API 同级**全走认证中间件**（复用 requireAuth）。
- 统计为**只读内存聚合**（D-WEB-5）：从 `store.ListSessions` + 按需 `store.LoadSession` 计算，无新增存储。

## 变更清单

### 1. internal/webserver/webserver.go — 新增 stats 处理
- 新路由：`GET /api/stats`（走认证）。
- `statsView` 结构（JSON）：
  - `sessions_total int`
  - `events_total int`
  - `last_active time.Time`（最近事件时间；无会话 → zero）
  - `event_type_counts map[string]int`（跨全部会话的事件类型计数）
  - `tool_calls int`（`tool/result` 事件总数）
- 实现：`ListSessions` → 每个 session `LoadSession` → 遍历事件累加（event_type_counts、tool_calls、events_total、last_active=max At）。事件多时可能重，但个人门户可接受；实现内加注释说明是 O(全部事件) 只读聚合。
- 注意：ListSessions 返回所有会话，逐个 LoadSession 全量事件——大量会话/事件时慢。本期接受（个人门户规模），不加分页（诚实记录限制）。

### 2. internal/webserver/static/app.js — dashboard 页面
- `#/dashboard` 从占位改为真实渲染：fetch `/api/stats` → 显示会话总数、事件总数、工具调用数、最近活动时间 + 事件类型分布（类型→计数，简单列表或原生 DOM 柱状条，不引图表库）。
- 保持 hash 导航；`#/kb` 仍是占位（M10b）。
- 样式用 style.css 已有类，可加少量新类（如 `.bar`、`.bar-row`）。

### 3. internal/webserver/webserver_test.go — 新增测试
- `TestStats`：造 2 会话（各若干事件，含 tool/result）→ `/api/stats` 断言 sessions_total/events_total/tool_calls/event_type_counts 正确。
- 空库：`TestStatsEmpty` → sessions_total 0、events_total 0、tool_calls 0。
- 认证：`/api/stats` 无 token → 401（复用 doReq 即可，不必新测试——在 TestStats 或单独加一行）。

### 4. 提交
`M10c: dashboard 工作台（/api/stats 只读聚合 + 前端 dashboard 页面）`

## 验证
`go build ./...` + `go vet ./internal/webserver/` + `go test -count=1 -timeout 90s ./internal/webserver/ -v` 全 PASS 后提交；随后 `go test -count=1 -timeout 90s ./...` 全绿确认（env 同 M10a）。

## 报告（简短）
提交 hash + go test 结果 + 偏离说明。不要贴代码。

# M10 升级：Web 全功能工作台（对齐 dsh web）ADR

- 状态：已定
- 日期：2026-08-20
- 背景：`2026-08-20-m10-web-portal.md`（M10a/c/b 只读门户，已交付）
- 触发：用户实测门户后要求「完全像 dsh 一样的全功能 web」——dsh 的 web 是 Agent 的实时工作台（发消息 + 流式回显 + 工具卡片 + 会话管理 + 设置），而当前 M10 是只读浏览（D-WEB-4）。

## 背景

dsh web 的核心功能面：实时对话（发消息 + 流式 assistant 输出 + 结构化工具卡片）、会话管理（列表/新建/恢复）、事件/消息流浏览、设置（provider/model/caps）、统计 dashboard。personal-agent 的 M10 目前只有只读浏览 + dashboard + KB 空壳。

要让 personal-agent 达到 dsh 级，最大缺口是**交互**：web 发消息 → agent 执行 → 流式回显。这需要把 web 变成**第二交互面**（与 REPL 并列），触碰「REPL 是唯一交互面」的历史假设，但**可以不改 loop 核心**（D4 保持）——因为：

- `Loop.Run(ctx, text)` 是公开的 turn 入口（loop.go:104），REPL 每次 `a.newLoop().Run` 驱动它；
- 每次流式 chunk **已落库** `assistant/chunk`（loop.go:240）+ `onText`/`onError` 回调（loop.go:237/229）；
- `a.log.SetSink` 是唯一持久化桥（`attachSink` → `store.AppendEvents`），cmd/pa 可在 sink 上挂广播器；
- 执行面天然串行（D5）：REPL 在 `scanner.Scan()` 阻塞等待时不在 Run 中，web 可在此时拿锁触发 Run。

## 决策（D-WEB2-A ~ D-WEB2-F）

### D-WEB2-A web 成为第二交互面：发消息 + 串行共享 loop（loop 不改，D4 保持）
- 新 cmd/pa 方法 `runTurn(ctx, text string, interactive bool) error`：`turnMu.Lock(); defer turnMu.Unlock(); loop := a.newLoop(); loop 的 OnText 仅在 interactive 时 fmt.Print（web 触发时静默——chunk 已落库，SSE 事件流负责回显）；loop.Run(ctx, text)`。
- REPL 循环（main.go repl 579 行）改用 `a.runTurn(ctx, line, true)`；web handler 用 `a.runTurn(ctx, text, false)`。**任何时刻至多一个 Run 在执行**（turnMu），D5 保持；loop.go 零改动。
- 新 API：`POST /api/sessions/{id}/message {text}`：若 `id != a.currentID` 先 `resumeSession`；然后 `runTurn(ctx, text, false)`；返回 200（Run 完成，前端靠 SSE 看流式过程）。

### D-WEB2-B 实时回显 = SSE 事件流（复用已落库的 chunk）
- 流式 chunk 已逐段落库（`assistant/chunk`），工具结果/错误落库（`tool/result`、`tool/error`）。前端「实时」只需**订阅新事件**。
- 新 cmd/pa `eventHub`：`attachSink` 的 sink 在写 store 时同时 `hub.Publish(ev)`（当前会话的新事件广播给订阅者）。SSE 端点在 `internal/webserver` 侧经 Package-private 通道拿到 hub？——**接线决策**：hub 放 cmd/pa（拥有 log/sink 的地方），`internal/webserver` 增加一个 `SetEventSource(func(sessionID string, sink func(session.Event)) (unsubscribe func()))` 注入点（webserver 不依赖 cmd/pa；cmd/pa registerWebServer 时注入 hub 订阅器）。这样 webserver 保持通用（只读 Store 服务 + 可注入的事件源），cmd/pa 提供实时源。
- 新 API：`GET /api/sessions/{id}/events/stream`（SSE `text/event-stream`）：先发已有事件快照（LoadSession），再推该会话新事件（hub 过滤 sessionID）；每事件 `data: {seq,type,time,summary}` + `\n\n`；连接断开自动退订（context 取消）。全 API 走 bearer 认证（SSE 首请求带 token）。
- 工具卡片：前端按 `type` 渲染——`user/message`/`assistant/message` 文本气泡、`assistant/chunk` 流式追加到当前气泡、`tool/result`/`tool/error` 折叠卡片（name + 有界 output）。

### D-WEB2-C 会话管理 API（复用 cmd/pa 现有方法）
- `POST /api/sessions` → `a.newSession(ctx)`（返回新 id）。
- `POST /api/sessions/{id}/resume` → `a.resumeSession(ctx, id)`。
- 列表 `GET /api/sessions` 已有。前端：新建 / 恢复 / 切换会话 + 对话区。

### D-WEB2-D 设置页 = 只读脱敏展示（无运行时热改，诚实架构限制）
- `GET /api/config`：从 `a.cfg` 生成脱敏视图（model、base_url、llm.provider、mode、各能力 cap enabled、web_server.addr、tools 白名单数量等；**永不返回 token/key**——token 字段一律 `***`）。
- 前端设置页只读展示 + 提示「改 `config.yaml` 后重启生效」。不做运行时编辑（配置重启生效是既定模型，D10；不引入热重载）。
- 复用 `/llm-status` 的只读心智（llm-status 是 REPL 命令——web 的 config API 独立实现，不调 REPL）。

### D-WEB2-E 排除：插件 / Slots / 主题运行时（架构边界，诚实声明）
- dsh 的 Cordis 插件 / Slots / 运行时主题在 personal-agent 不存在（编译期接缝 + 无插件运行时，用户已拍板排除「创造模式」）。
- web 提供**前端主题**（深/浅色 CSS 变量切换，localStorage 记忆）——这是静态前端能力，非运行时插件。
- 不承诺 dsh 的插件面板、主题商店等。

### D-WEB2-F 前端整体重构为 dsh 式聊天工作台（唯一主界面，取代只读浏览 UI）
- **用户拍板（2026-08-20）**：「现在的只读 web ui 不需要，新的 web 需要像 dsh web 完全一样」——前端不再保留独立的只读「会话列表 / 事件流浏览 / dashboard」主页面；重构为 dsh 式**聊天工作台**为唯一主界面：
  - **左侧会话栏**：会话列表（新建 / 恢复 / 切换，复用 D-WEB2-C API）；
  - **主区聊天**：user/assistant 消息气泡 + 流式追加（SSE chunk 逐字进当前气泡）+ **工具调用卡片**（tool/result、tool/error 折叠块，name + 有界 output）；
  - **顶部/侧栏**：模型 / provider 显示、`#/settings` 设置入口、深/浅主题切换（D-WEB2-F 前端主题）。
  - 独立的只读浏览页（M10a 的会话列表页 / 事件流页 / M10c 的 dashboard 统计页）**不作为主页面**；其后端 API（sessions / events / stats）**保留**，供聊天工作台内部使用（会话栏、统计可并入侧栏/设置辅助）。
- vanilla JS 保持，零新依赖、无构建。
- **SSE 认证接线（D-WEB2-B 续）**：浏览器 `EventSource` 不能设置 `Authorization` header（且 token 入 URL 有泄露面）→ **决策：前端用 `fetch` + `response.body.getReader()` 手动解析 SSE 帧（~30 行）**——token 只经 Authorization header，SSE 端点与普通 API 共用同一 `requireAuth` 中间件。

### D-WEB2-G 认证改为默认直开，token 可选（用户拍板 2026-08-20）
- 见 `2026-08-20-m10-web-portal.md` D-WEB-2 变更：`web_server.token` 为空 → 所有 API 放行（信任本机 `127.0.0.1`，与 dsh web 一致）；填了才走 bearer 校验。
- 前端：**移除登录视图**（token 为空时直进工作区）；仅当后端实际要求认证（token 配置了）才提示输入 token（存 localStorage 附到 fetch）。

### D-WEB2-H 工作区扩展为 dsh 级完整功能面（用户拍板 2026-08-20：要 dsh web 工作区的全部功能）
- 对齐 dsh web 工作区：**左侧面板（可 tab：会话 / 子代理 / 后台任务）+ 主聊天区 + 顶部栏（会话模式 + 模型/provider + 主题）**。
- **消息流全元素**：user/assistant 气泡 + **思维链（reasoning 折叠块，assistant/message 的 M8 reasoning 落库）** + 工具调用块（tool/result/error）+ 流式 chunk + 时间戳。
- **顶部会话模式**：显示当前 `mode`（standard/minimal/code，来自 config）；切换提示「改 config.yaml 重启生效」（无热重载，诚实限制）。
- **子代理列表**：只读展示活跃子代理（id/状态/创建时间/描述），数据来自 cmd/pa 子代理运行时状态（脱敏，D7）；**后台任务列表**：只读展示 jobs 引擎状态（id/状态/创建/结果摘要，脱敏，D7）。
- **设置全功能**：模型 / provider / base_url / mode / 各 cap enabled / tools 白名单 / web addr（`GET /api/config` 扩展）+ 主题。
- **会话管理**：新建 / 恢复 / 切换 +（可选）重命名 / 删除。
- **视觉**：现代深色主题（CSS 变量 + 精致间距/卡片/动效），对齐 dsh 观感；vanilla JS 保持（零新依赖、无构建、单二进制）。
- 后端只读状态 API（subagents / jobs）与 `GET /api/config` 扩展均走脱敏（不泄露密钥/敏感事件正文）。

## 理由

1. **dsh 级工作台的核心是交互**：只读浏览（M10 现状）与 dsh 相差一个「对话面」。D-WEB2-A 让 web 发消息且**不改 loop**（D4 铁律保持）、**不破坏串行**（D5 turnMu），风险最小。
2. **流式复用已落库的 chunk**：loop 每 chunk 已落 `assistant/chunk`，SSE 只做「订阅+转发」，零新存储、loop 零改动；比给 loop 加回调（触碰 D4）更稳。
3. **事件源在 cmd/pa、webserver 通用**：`internal/webserver` 保持「只读 Store + 可注入事件源」，不反向依赖 cmd/pa；注册时注入 hub。分层干净。
4. **SSE 用 fetch 流解析**：保住「token 只在 header」的认证模型（不放 URL），复用 requireAuth。
5. **排除项诚实声明**：插件/Slots/热重载是架构既有限制，不假装支持；前端主题是静态能力，可做。

## 后果

- **好处**：达到 dsh 级工作台（实时对话 + 工具卡片 + 会话管理 + 设置 + dashboard）；REPL 与 web 共享同一 loop 与串行语义；零新依赖、CGO-free。
- **代价/限制**：REPL 与 web 发消息互斥（同时只能一个 turn 跑，D5）；配置改动重启生效（无热重载）；插件/Slots 无；SSE 为 fetch 流解析（前端代码略增）。
- **放弃的方案**：① 给 loop 加流式回调（触碰 D4）；② token 放 URL 的 EventSource（认证模型被削弱）；③ 运行时热改配置（架构限制）；④ 引入前端框架/SSE 库（零依赖纪律）。

## 分段派发计划

- **W1 交互核心**：cmd/pa `turnMu` + `runTurn`（REPL 改用）+ `eventHub` + registerWebServer 注入事件源 + webserver `POST /api/sessions/{id}/message` + `GET /api/sessions/{id}/events/stream`（SSE）+ 前端 `#/chat`（消息流 + 输入 + fetch 流 SSE + 工具卡片）。验收：REPL 与 web 各自/交错发消息串行正确；SSE 实时推 chunk/工具结果；认证生效。
- **W2 会话管理 + 设置 + 主题**：`POST /api/sessions`（new）+ `POST /api/sessions/{id}/resume` + `GET /api/config`（脱敏）+ 前端会话侧栏管理 + `#/settings` 页 + 深/浅主题。验收：web 新建/恢复会话与 REPL 一致；settings 脱敏正确（无 token 泄露）；主题切换记忆。
- **W3 收尾**：全量验收（vet/build/test + 真实 HTTP 冒烟：SSE 流式 + 发消息 + 会话管理）+ Agent.md/design.md 更新。

## 下一步

写 `docs/dispatch-m10-web2.md`（W1 契约）→ 派发 → 控制器验收 → W2/W3。

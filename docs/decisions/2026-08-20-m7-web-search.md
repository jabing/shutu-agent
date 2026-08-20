# ADR 2026-08-20：M7 web 搜索——联网能力接缝（DeepSeek 官方搜索）

> 状态：已定 · 背景 → 决策 → 理由 → 后果（含放弃的方案）

## 背景

M6 六段完成后，个人 Agent 的信源只有模型训练知识（有过时缺陷）+ 本地知识库（kb）+ fs + MCP，**无联网能力**。2026-08-20 评估 deepseek-harness rc.8 时用户拍板：web 搜索列入路线图（§4 候选 M7），后端走 **DeepSeek 官方搜索**——dsh 的 `packages/web/web-search-deepseek/` 通过 DeepSeek **Anthropic 兼容 Messages API**（`POST /anthropic/v1/messages`）+ 原生 `web_search_20250305` server tool 实现，复用 `DEEPSEEK_API_KEY`（零新密钥），服务器端执行搜索返回**结构化** `web_search_tool_result` blocks。用户定序：M7 属"Agent 部分"，最先做。

## 决策

新增 `internal/web` 能力接缝，走 Service/Provider/Tool 三件套 + 事件 + config + D10 默认关，对照 dsh `packages/web/{web,web-search-deepseek,web-fetch-http,tool-web}/`：

| 部件 | 交付物 | 对照 dsh |
|---|---|---|
| **Service** | `web.SearchProvider` / `web.FetchProvider` 接口（单查询/单 URL 契约）+ 注册表 + Engine（选择 provider、`maxResults` 截断）+ `WebError` sentinel 错误码 | `packages/web/web/src/types.ts` |
| **搜索 Provider** | `DeepSeekSearchProvider`：Anthropic 兼容 Messages API 非流式 POST + `web_search_20250305` server tool，解析 `web_search_tool_result` blocks → 规范化 `WebSearchResult`；key 从 `DEEPSEEK_API_KEY` 环境变量读（env-only，纪律 6） | `web-search-deepseek/src/provider.ts` |
| **抓取 Provider** | `HttpFetchProvider`：URL 校验、同源重定向、超时、字节/字符上限、content-type 分类（html/text）→ `WebFetchResult` | `web-fetch-http/src/provider.ts` + `policy.ts` |
| **HTML→文本** | 轻量 HTML→Markdown 转换（**零依赖**，标准库 `html` + 手写状态机；覆盖 h1-h6/p/a/strong/em/ul/ol/li/code/pre/blockquote/br/img alt，其余简化剥离） | dsh 用 turndown（npm 依赖），本项目裁剪为"够模型读" |
| **工具** | `web_search`（`queries` 必填数组，1..`maxQueries` 非空校验、去重、多查询扇出 + round-robin 合并 + URL 去重 + 截断）+ `web_fetch`（`url`）；D7 入口统一 JSON Schema 校验 | `tool-web/src/search.ts` + `fetch.ts` |
| **事件** | `web/search-request`（D3，secret-free：query/endpoint/model/body 快照，派发前落库） | dsh `web/deepseek-search-llm-request` |
| **config** | `web.enabled`（默认 false，D10）+ provider 参数（deepseek: baseURL/model/apiVersion/maxTokens/maxUses + apiKeyEnv；fetch: maxUrlLength/maxResponseBytes/maxBodyChars/timeoutMs/maxRedirects/userAgent）+ 工具上限（searchMaxResults=8 / searchMaxQueries=4 / fetchTimeoutMs=30s / fetchMaxOutputChars） | `tool-web/README.md` + 各 provider config |

**接线**：`cmd/pa` 新增 `registerWeb()`（enabled 时创建 provider + Engine + 注册工具白名单 + 事件 sink；disabled 零操作，D10）。走工具注册层，**不改 loop**（D4）。

## 理由

1. **联网是 M6 之后最明显的能力缺口**（模型训练时效 + 本地库有限），与 kb（显式知识）互补，还能反向喂 KB 摄入。
2. **DeepSeek 官方搜索**：复用现有 key 零新密钥；服务器端结构化结果，无爬取/法律/稳定性顾虑；Anthropic 兼容端点与 M8（Anthropic 适配器）、M10（webServer）共享 HTTP 客户端心智。
3. **零新依赖**：全部用标准库 `net/http` + `html` + 手写转换；不引入任何第三方（HTML 转换不引 turndown 等价物）。
4. **接缝三件套 + 默认关**：与 M4–M6 同款，零循环改动（D4），符合 D10。

## 后果

- **放弃**：并发扇出（dsh 用 `Promise.allSettled` 并发跑多个查询）。本项目 D5 主循环严格串行，M7 多查询用**顺序扇出**（`for` 逐个查询，出错即停），语义与 dsh 相同（去重 + round-robin 合并 + 截断），代价是 N 个查询串行耗时——个人场景 `queries ≤ 4` 可接受。保留"未来如需并发，工具内部 bounded fan-out 不经后台 goroutine 触碰会话日志"的余地。
- **裁剪**：HTML→Markdown 是简化实现（非完整规范）：覆盖常见文档结构、够模型阅读；不追求排版保真（表格/嵌套复杂度简化）。质量上限记录于此，避免后续误以为完整渲染。
- **安全**：fetch provider 只取公开 http(s) 资源，无 cookie/凭据；`maxResponseBytes`/`maxBodyChars` 硬上限防爆上下文；`web_fetch` 与 `web_search` 默认关（D10）。SSRF/内网可达性记录为已知限制（同 dsh 注释"不实现私网保护"），个人单机默认可信。
- **凭证**：`DEEPSEEK_API_KEY` 只走环境变量（纪律 6），绝不落库/落日志；`web/search-request` 事件只记 secret-free 快照。
- **与 M8 关系**：M7 写 Anthropic 兼容 HTTP 客户端（非流式 JSON），M8 的 Anthropic Messages 适配器可复用同一心智/可能复用请求构造；M7 不依赖 M8。
- 本 ADR 与 design.md §11、Agent.md §4 同步更新（双向同步）。

## 验收标准（M7 达标才算完成）

1. `go vet ./...` + `go test -count=1 ./...` + `go build ./...` 全绿；零新增第三方依赖；CGO-free。
2. 接缝：注册表可注册多 provider；`maxResults` 截断在 Engine 层；`WebError` sentinel 码语义正确。
3. DeepSeek provider：真实 key 下对已知查询返回结构化 `sources[]`（url/title/snippet/publishedAt）；无 result block 时 fail-closed 报 `WEB_PROVIDER_ERROR`（不退化到散文抓取）；凭证缺失报 `WEB_PROVIDER_CREDENTIAL_MISSING`。
4. `web_search` 工具：单查询返回 provider 结果；多查询去重 + round-robin 合并 + 截断；超 `maxQueries`/空串/重复输入按 D7 拒绝；错误中止剩余查询。
5. `web_fetch`：公开 URL 抓取成功返回分类 body；非 2xx 是结果非错误；重定向/超时/超限各报对应错误码；HTML 转 markdown 可读。
6. D3：`web/search-request` 事件在派发前落库且含 query/endpoint/body 快照，不含 key。
7. D10：`web.enabled` 默认 false；disabled 时零注册零事件。
8. 不改 `internal/loop/loop.go`（D4）；主循环保持串行（D5）。

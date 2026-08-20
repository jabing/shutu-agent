# Agent.md — 个人 Agent 项目全局规划

> 本文是项目工作入口：状态、路线图、开发纪律都在这里。
> 设计基线在 [`docs/design.md`](docs/design.md)——**改设计先改那里，再改代码**。

---

## 1. 项目定位

Go 实现、借鉴 DeepSeek Harness 架构的个人 Agent：薄核心（会话日志 + LLM 适配 + 工具注册表 + 提示词组装 + 循环），后期以"能力接缝"方式接入个人知识库（RAG）。参考实现：`../deepseek-harness`（重点读 `docs/architecture.md`、`docs/subsystems/core.md`、`docs/subsystems/session.md`）。

## 2. 设计基线（防漂移摘要，细节见 design.md）

- **D1** 会话 = 追加式事件日志，历史是派生值，永不另存。
- **D2** 新能力 = Service / Provider / Tool 三件套，消费方只依赖接口。
- **D3** 模型可见 ⇒ 已落日志；新模型可见输入 ⇒ 新事件类型。
- **D4** 薄核心；v1 用 Go 接口 + 注册表，不引入插件系统/事件总线。
- **D5** 循环串行同步；并发、后台任务推迟到 M5（有明确用例才做）。
- **D6** LLM 适配器第一天就支持 SSE 流式。
- **D7** 工具参数在 Execute 入口统一 JSON Schema 校验。
- **D8** 持久化走 store 接口（SQLite 后端，CGO-free），事件带版本号。
- **D9** 知识库是能力接缝（kb service + 可换 Provider + kb_* 工具）；检索与对话模型解耦（M4 用 FTS5 全文检索 + 提取回写，向量/embedding 仅作 M5 可选 Provider 预留）。
- **D10** 安全白名单先行；执行类工具（bash 等）M3 才上。

## 3. 当前状态

**2026-08-18**：M3 完成并通过验收（提交 `1dda2ed`，ADR `2026-08-18-m3-sandbox-scope`）。M4 知识库**三段全部完成并通过验收**：M4a 内核（`682f07e`）、M4b 工具与召回（`bdd903d`）、M4c 提取回写（`5e98fa7`）。方向：**参照 dsh-knowledge（已下载 `../dsh-knowledge`，FTS5 全文检索 + 提取回写，非向量 RAG，方案已实测）**，调研见 `docs/research-m4-kb.md`，派发见 `docs/dispatch-m4a/b/c.md`，ADR `2026-08-18-m4-kb-architecture.md` 完整定稿七项决策。

**2026-08-19**：M5 核心能力启动（用户拍板"必须、先实现"；ADR `2026-08-18-m5-agent-core.md` 定稿四段决策）。**M5 四段全部完成并通过验收**（每段由控制会话亲自 vet/test/build 验收、对照 D1–D10 审 diff）：M5a 后台任务（M5a-1 `34bf1e8`+`5f3abd4`、M5a-2 `4c0a25e`+`dbe07fc`+`b1d3535`+`6d91af7`）→ M5b 子代理（`55f1b63`+`34c302c`+`8a3f648`、`8c7f1b3`+`070039e`+`27adaca`+`e8dcec0`+`78fd6a6`）→ M5c 上下文压缩（`76c41db`、`2188b4d`+`0ffa4e4`+`4669c2e`、`a5219ac`、`c4b5e88`+`e9b2b9c`）→ M5d 技能（`b2d93fc`+`453b288`+`0c38de5`+`400e06c`+`75d892c`、`cb09853`+`17cfe10`+`935ffdc`+`6859000`+`07a82ce`）。

**2026-08-19（续）**：与 dsh 差距评估——M5 后除知识库/Web 接口外，个人 Agent 的实质能力缺口为：定时调度、任务规划、长期记忆、人工审批（任务类）+ 代码沙箱、工具生态/fs 封装（代码类）。用户拍板"需要补全"→ 定稿 **M6 能力补全六段**（ADR `2026-08-19-m6-agent-full.md`）：M6a 定时调度 → M6b 任务规划 → M6c 长期记忆 → M6d 人工审批 → M6e 代码沙箱 → M6f 工具生态。全部接缝挂薄核心（D4）、默认关（D10）、零新依赖（M6f MCP 自实现 JSON-RPC over stdio）。**M6 六段全部完成并通过验收**（M6a `85cd9a3`…`3fb43fd`；M6b `e006a9e`…`512896f`；M6c `b087b22`…`717ed92`；M6d `6d32daa`…`0118169`；M6e `a66d33e`…`cb39660`；M6f `764261c`…`f20ae3b`）。

**2026-08-20 冒烟**：真实端到端冒烟（pa.exe 全链路，用户拍板"①"）。A. 无 API key 启动正确报错退出（env-only 约束生效）；发现 `data/pa.db` 留存 M4 时期的真实运行会话（4 轮对话 + `kb/extract`/`kb/recall`/`kb/add`/`tool/result`，D1 落库曾实测）。B. 真实对话（DeepSeek 流式）成功——/help 完整命令表 + 中文回答，会话 332→375 事件落库、重启恢复（resumed session）验证通过。C. 临时 config 启用 fs：模型真实调用 `fs_write` 创建 `smoke-c.txt` + `fs_read` 读回，`fs/write`/`fs/read`/`tool/result` 事件全部落库（工具注册→白名单→D3 事件→D7 校验→执行→落库整链路实测）。冒烟产物已清理（`.smoke/`、`pa-smoke.exe`、`smoke-c.txt`），工作树干净。

**2026-08-20 rc.8 评估**：检查 deepseek-harness 上游更新（本地克隆 rc.7→rc.8，`dsh-v0.1.0-rc.8`，浅克隆 `141eb6f`）。逐项评估后**无必须跟进项**：① SQLite chunk-row 压缩（93b4b98，250 万逻辑事件→6.6 万物理行、体积降 89%）与本项目逐 chunk 落库的行放大同源，但个人规模量级差太大（实测 pa.db 73KB、chunk 占 93% 行但每条 data 仅 16B；5-10 年估算 ~100MB–1GB）、跟进需 Zstandard 第三方依赖，暂缓（触发条件：单库 >数 GB 或可感知变慢；更轻替代：WAL+VACUUM → 会话归档 → 完整 message 落库+断流恢复，而非 chunk packing）；② DeepSeek reasoning 回传（583894f）单 provider 无网关时不必要，但**做多 provider 后成为必要**（并入 M8）。用户拍板：**web 搜索列入路线图**（见 §4 候选小节），后端走 **DeepSeek 官方搜索**（dsh `packages/web/web-search-deepseek/`：Anthropic 兼容 Messages API `POST /anthropic/v1/messages` + 原生 `web_search_20250305` server tool，复用 `DEEPSEEK_API_KEY` 零新密钥，服务器端搜索返回结构化 `web_search_tool_result`，代价=每次搜索一次完整模型调用）。

**2026-08-20 路线图决策**：用户拍板四项列为候选（见 §4）：**pwsh persistent PTY**（dsh rc.8 新增 `tool-pwsh-persistent`，owner-scoped 持久 shell，cwd/env/函数跨调用保留）、**多 LLM provider**（必做）、**deepseek reasoning 回传**（依附多 provider：跨 provider 重编码会话时需要，`llm.Message` 需带 `reasoning_content` 落库并回传）、**多模态**（必要；dsh `7078918` 范式：落库只存 `ImageAttachmentRef`、data URL 仅请求时存在、20MiB 上限、模型能力按 exact-model `inputModalities` 声明）。**组织判断**：多 provider + reasoning 回传 + 多模态三件都改 `llm.Message` 消息模型与 wire 层，打包为 **M8 消息模型升级** 一次设计，避免改三次；persistent PTY 独立为 **M9**。

**2026-08-20 Web 门户决策（翻转"Web 延后"）**：用户拍板——知识库 Web 管理界面 + dsh web 功能都需要，目标是完整的个人工作台（知识库查询、真实解决问题、业务数据查询、写脚本、dashboard 可视化）。**翻转** M1 `design.md:19`"第一版就做 Web UI → REPL 先磨循环"、M3 dispatch"Web 明确不做"、M6 ADR"remote API/SDK 暂缓"三条历史决策。落地形态（见 §4 候选）：**KB 全量**（dsh-knowledge 核心功能层，不含 web 层——web 层由本项目自建）+ **M10 Web 门户**（webServer 基础设施 → 知识库 Web 管理台 → dashboard 工作台）。dsh-knowledge 的 web 层依赖 DSH `webServer`/Client Slots 平台，本项目无此平台，故**借鉴其功能面、用 Go 标准库自建**（零新依赖）。

## 4. 路线图

| 里程碑 | 交付物 | 验收标准（达标才算完成） | 状态 |
|---|---|---|---|
| **M1 最小循环** | `cmd/pa` REPL；`llm`（DeepSeek 流式）；`session` 内存日志；`tools` 注册表 + `get_time`/`read_file`；`loop` 串行 turn/step | 命令行提问可流式回答；工具可被调用并回写日志；`go vet` + `go test` 干净 | ✅ 2026-08-18 验收通过（`6380163`） |
| **M2 持久化与会话** | `store`（SQLite）+ 多会话（/new /list /resume）；`prompt` 分节组装；`config.yaml`；重试策略 | 重启恢复会话且历史完整回放；新事件类型不改历史结构 | ✅ 2026-08-18 验收通过（`e865aca`） |
| **M3 安全与完善** | 工具白名单/权限；超时与输出截断；取消（Ctrl+C）；CLI 完善（Web 可选） | 未白名单工具拒绝执行；取消即时生效；长输出不爆上下文 | ✅ 2026-08-18 验收通过（`1dda2ed`，ADR `2026-08-18-m3-sandbox-scope`） |
| **M4 知识库**（三段） | 拆为 M4a/b/c 依次验收 | 全部达标才算 M4 完成 | ⬜ |
| **M4a 内核** | `kb` 接口（Search/Add/Recall）+ SQLite FTS5 Provider（BM25 + 中文二元组 LIKE 兜底）+ `kb/recall` 事件类型 + config；主 ADR 定稿检索方案 | 中文/英文/混合检索正确；`Add` 后能检索；换 Provider 不改消费方；零新依赖 | ✅ 2026-08-18 验收通过（`682f07e`，ADR `2026-08-18-m4-kb-architecture.md`） |
| **M4b 工具与召回** | `kb_search`/`kb_read`/`kb_add` 工具（默认关）+ `cmd/pa` 召回注入（catalog + 有界 recall）+ `/kb-status` `/kb-reindex` + `kb/add` 事件 | 工具默认关闭且参数校验；注入走 `kb/recall` 落日志；fail-open | ✅ 2026-08-18 验收通过（`bdd903d`） |
| **M4c 提取回写** | `KB.Extract`（幂等 `session:turn`、严格 JSON fail-closed、不阻断回答）+ `kb/extract` 事件 + config；补 ADR | 对话产生可复用知识能被提取写入并被后续检索；坏输出 fail-closed | ✅ 2026-08-18 验收通过（`5e98fa7`） |
| **M4 知识库**（三段） | 拆为 M4a/b/c 依次验收 | 全部达标才算 M4 完成 | ✅ 三段全部完成 |
| **M5 核心能力**（四段，ADR `2026-08-18-m5-agent-core.md`） | 拆为 M5a/b/c/d 依次验收 | 全部达标才算 M5 完成 | ✅ 四段全部完成（M5a/M5b/M5c/M5d，均 2026-08-19 验收通过） |
| **M5a 后台任务** | `jobs` 接口（owner-fenced 注册表）+ 本地实现 + `job_*` 工具 + `job/*` 事件 + config | 后台工作可观察/取消/等待/通知；owner 隔离；主循环保持串行；默认关闭 | ✅ 2026-08-19 验收通过（M5a-1 `34bf1e8`+`5f3abd4`；M5a-2 `4c0a25e`+`dbe07fc`+`b1d3535`+`6d91af7`） |
| **M5b 子代理** | `subagent` 接口（多 Provider 注册表）+ spawn 实现 + 委托/控制/报告工具 + `subagent/*` 事件 + config | 子代理独立会话日志可回放；结果回传父会话；后台续跑走 job；默认关闭 | ✅ 2026-08-19 验收通过（M5b-1 `55f1b63`+`34c302c`+`8a3f648`；M5b-2 `8c7f1b3`+`070039e`+`27acada`+`e8dcec0`+`78fd6a6`） |
| **M5c 上下文压缩** | `compaction` 接缝 + 摘要 provider + tool-result 剪枝 + `/compact` + `compaction/*` 事件 + config + PreStep 自动压缩 | 超预算触发压缩；摘要经 surfaceOp.replace user/message 遮蔽旧范围且日志仍追加式；tool-call/result 配对不被切断；默认关闭 | ✅ 2026-08-19 验收通过（M5c-1a `76c41db`；M5c-1b `2188b4d`+`0ffa4e4`+`4669c2e`；M5c-2a `a5219ac`；M5c-2b `c4b5e88`+`e9b2b9c`） |
| **M5d 技能** | `skill` 接口（多 Provider 注册表）+ 文件系统发现 + 目录注入 + `skill` 加载工具 + `skill/*` 事件 + config | 目录注入有界；按需加载完整正文；默认关闭 | ✅ 2026-08-19 验收通过（M5d-1 `b2d93fc`+`453b288`+`0c38de5`+`400e06c`+`75d892c`；M5d-2 `cb09853`+`17cfe10`+`935ffdc`+`6859000`+`07a82ce`） |
| **M6 能力补全**（六段，ADR `2026-08-19-m6-agent-full.md`） | 拆为 M6a/b/c/d/e/f 依次验收 | 全部达标才算 M6 完成 | ✅ 六段全部完成（M6a/M6b/M6c/M6d/M6e/M6f，均 2026-08-19 验收通过） |
| **M6a 定时调度** | `schedule` 接口（多 Provider 注册表）+ 间隔/cron 实现 + `schedule_*` 工具 + `schedule/*` 事件 + config | 定时任务到期触发（事件 + 入队 job，D5）；可观察/取消；默认关闭 | ✅ 2026-08-19 验收通过（M6a-1 `85cd9a3`+`5aeb9e5`+`ef9011a`；M6a-2 `2d5aed4`+`d599e4f`+`84b0346`+`3fb43fd`） |
| **M6b 任务规划** | `plan` 接口（goal→plan→todo 三层）+ 规划/推进工具 + `plan/*` 事件 + config | 多步任务拆解/跟踪/推进（执行可委托子代理）；默认关闭 | ✅ 2026-08-19 验收通过（M6b-1 `e006a9e`+`eaf13e9`；M6b-2 `69e57cd`+`437028c`+`1b6b62b`+`512896f`） |
| **M6c 长期记忆** | `spill` 接口（跨会话记忆 Provider）+ 自动沉淀/召回 + `spill/*` 事件 + config | 对话衍生记忆自动沉淀并可召回；与 kb（显式知识）接缝独立；默认关闭 | ✅ 2026-08-19 验收通过（M6c-1 `b087b22`+`949c84e`+`4ae6a42`；M6c-2 `f88ad7b`+`9f80bf8`+`32ec136`+`717ed92`） |
| **M6d 人工审批** | `interact` 接口（审批请求/响应）+ 敏感工具门 + `interact/*` 事件 + config | 敏感操作执行前经人工确认（CLI y/n，fail-closed）；默认关闭 | ✅ 2026-08-19 验收通过（M6d-1 `6d32daa`+`d277ba2`；M6d-2 `8a3ad1b`+`6cd032a`+`0b01683`+`fb578e3`+`0118169`） |
| **M6e 代码沙箱** | `code` 接口（沙箱 Provider）+ 本地子进程隔离实现 + `code_run` 工具 + `code/*` 事件 + config | 模型生成代码在受控沙箱执行（超时/配额/默认无网络）；补强 M3 `run_command`；默认关闭 | ✅ 2026-08-19 验收通过（M6e-1 `a66d33e`+`24d7f1c`；M6e-2 `be9ecf2`+`e850820`+`cf2590f`+`cb39660`） |
| **M6f 工具生态** | `mcp` 接口（MCP 客户端，JSON-RPC 自实现优先）+ `fs`/workspace 统一封装 + 工具 + `mcp/*` 事件 + config | 外部工具/服务经 MCP 接入；文件操作统一封装；默认关闭 | ✅ 2026-08-19 验收通过（M6f-1 `764261c`+`4e474f2`；M6f-2 `29ea541`+`ef92769`+`0e025fc`+`a5a9494`；M6f-3 `8526f59`+`c3a74a0`+`9e09d9e`+`f20ae3b`） |

### 候选里程碑（未定序，2026-08-20）

> 依赖关系：KB 全量 → M10 管理台（多库/挂载/标签/条目管理是其展示前提）；M8 打包 多 provider + reasoning 回传 + 多模态（同改 `llm.Message` 消息模型与 wire 层）；M9 持久 PTY 经 jobs（M5a）owner-fenced 承载；M10 用 Go 标准库 `net/http` 自建（零新依赖），借鉴 dsh-knowledge web 层功能面。

| 候选 | 交付物 | 验收标准（达标才算完成） | 状态 |
|---|---|---|---|
| **KB 全量**（dsh-knowledge 核心功能层，不含其 web 层） | 多知识库（各自说明/默认标签/提取要求）+ 会话/项目挂载（继承/覆盖/关闭）+ 包含/排除标签 + 全局"严谨/主动"回写策略 + 直写协调（create/update/conflict/skip、同主题合并留版本、完全重复跳过、疑似冲突转审核）+ 条目 List/Delete/Markdown 导出 + `/kb-ingest` 文档摄入（含 web 页面）+ `knowledge_search`/`read`/`base_create`/`base_update` 工具 + 事件 + config | 多库/挂载/标签生效；回写策略与直写协调正确；摄入文档可检索；条目可管理；默认关闭（D10）；零新依赖 | ⬜ 候选 |
| **M7 web 搜索** | `web` 接缝三件套（web service + WebSearchProvider 注册表 + `web_search`/`web_fetch` 工具）+ `web/*` 事件 + config；**DeepSeek 官方搜索 provider**（照搬 dsh `web-search-deepseek`：Anthropic 兼容 Messages API `POST /anthropic/v1/messages` + `web_search_20250305` server tool，复用 `DEEPSEEK_API_KEY`，解析结构化 `web_search_tool_result`）；多查询一步到位（seam 单查询契约 + 消费者侧扇出/去重/round-robin 合并，借鉴 rc.8） | 真实搜索返回结构化结果与来源；`web/*` 事件落库（D3）；按 D7 校验；默认关闭（D10）；零新依赖 | ⬜ 候选 |
| **M8 消息模型升级**（多 provider + reasoning 回传 + 多模态） | ① `llm.Message` 从 string Content 升级为 content parts（text / image ref / reasoning），assistant 消息支持 `reasoning_content` 落库（D3 新事件类型）并回传；② LLM provider 注册表 + config 选择（deepseek / OpenAI 兼容 / Anthropic Messages——与 M7 复用 Anthropic 兼容 HTTP 客户端）；③ 多模态：user 图片走文件路径→落库只存引用→请求时转 `image_url` data URL（dsh `7078918` 范式：20MiB 上限、最老替换占位符、PNG/JPEG/WebP/GIF、模型能力按 exact-model `inputModalities` 声明） | 可在 config 切换 provider 且会话历史跨 provider 重编码正确（reasoning 签名保留）；图片输入可被模型读取；`llm.Message` 相关 D3 事件类型新增且旧会话回放不受影响（D8）；默认关闭 | ⬜ 候选 |
| **M9 persistent PTY** | `terminals` 注册表（owner-fenced，经 jobs 承载，D5 串行访问）+ 持久 `pwsh` 工具（cwd/`$env:`/函数/登录态跨调用保留）+ `terminal/*` 事件 + config | 多步操作共享 shell 状态；超时关闭重置；输出上限；默认关闭（D10） | ⬜ 候选 |
| **M10 Web 门户**（webServer 基础设施 → 知识库管理台 → dashboard 工作台） | **M10a webServer 基础设施**：Go 标准库 `net/http` HTTP 服务 + 静态资源 + JSON API 路由 + bearer 认证（Token 存 SHA-256 摘要，dsh-knowledge 一致）+ 会话/事件浏览 API；**M10b 知识库 Web 管理台**：三栏文档界面（README/facts/decisions 视图）+ 条目搜索/维护 + 回写 AI 候选审核 + 客户端令牌管理（借鉴 dsh-knowledge `web/` 功能面，vanilla JS 静态前端）；**M10c dashboard 工作台**：知识库/会话/工具/搜索统计可视化 + 工作台入口（业务数据查询走已有 fs/code_run/MCP/kb/web 能力） | webServer 可服务静态页与 JSON API 且认证生效；管理台可浏览/检索/维护知识库条目并审核回写候选；dashboard 展示统计图表；默认关闭（D10）；零新依赖 | ⬜ 候选 |



## 5. 开发纪律（每轮工作前过一遍）

1. **新功能不改循环**（D4）：能力一律走接缝（接口 + 后端 + 工具）。
2. **模型可见必落日志**（D3）：先加事件类型，再实现。
3. **工具参数入口校验**（D7）：Execute 之前统一 JSON Schema 校验。
4. **先文档后代码**：涉及核心数据模型、循环结构、包依赖方向的变更，先写 `docs/decisions/` 决策记录并更新 design.md。
5. **保持 CGO-free**（Windows 可无工具链构建）；新依赖必须纯 Go 或可无 CGO 使用。
6. **API Key 只走环境变量**，绝不写入代码、配置或日志。
7. **双向同步**：design.md 与本文状态/决策变更必须同步更新。
8. **一里程碑一 PR/提交**：按验收标准检查后才算完成，不达标不进入下一里程碑。

## 6. 决策记录（ADR）

路径：`docs/decisions/YYYY-MM-DD-<slug>.md`。模板：状态（提案/已定/废弃）→ 背景 → 决策 → 理由 → 后果（含放弃的方案）。已有决策见 design.md 第 10 节 D1–D10，ADR 只记录其后的增量变更。

## 7. 常用命令

```sh
go build ./...        # 构建
go test ./...         # 单元测试
go vet ./...          # 静态检查
go run ./cmd/pa       # 启动 REPL（M1 后可用，需 DEEPSEEK_API_KEY）
```

## 8. 会话交接协议（控制面 / 实施面）

**分工**：本会话（控制面）定契约、验收、更新状态；实施会话（实施面）读契约、写代码、自测。会话间唯一可靠通信渠道是磁盘文件——新会话看不到控制会话的对话历史，只依赖本文档与 design.md。

**流程**：

1. **交接**：控制会话把开场白模板（见下）发给实施会话，指定里程碑；各里程碑的完整派发消息存于 `docs/dispatch-*.md`（M5 依序：`dispatch-m5a.md` → `dispatch-m5b.md` → `dispatch-m5c.md` → `dispatch-m5d.md`，均已完成派发；历史：`docs/dispatch-m4a/b/c.md`、`docs/dispatch-m3.md`、`docs/dispatch-m2.md`）。
2. **实施**：实施会话按 design.md 实现，自测通过后提交，并报告：改动文件清单、实现决策、跑过的命令、测试结果。
3. **验收**：控制会话亲自跑 `go build` / `go test` / `go vet`，审查 `git diff`，对照 D1–D10 逐条检查（日志先行、工具入口校验、接口隔离、无循环改动、无越界功能）。
4. **收尾**：通过 → 更新第 3/4 节状态 → 准备下一里程碑交接；不通过 → 把问题清单发回实施会话修订。

**实施会话开场白模板**（直接粘贴）：

> 请阅读 `D:\dev-projects\Agent\personal-agent\Agent.md` 和 `docs/design.md`，按设计基线实现 **M1 最小循环骨架**（里程碑验收标准见 Agent.md 第 4 节）。参考原型 dsh 的源码与文档在 `D:\dev-projects\Agent\deepseek-harness`——实现每个模块前先读 Agent.md 第 9 节对应的 dsh 源码与文档，借鉴其结构与接口设计（注意 dsh 是 TypeScript + 插件框架，只需借鉴思路，不照搬代码，Go 实现按 design.md 的模块地图落地）。完成后运行 `go vet ./...`、`go test ./...`、`go build ./...` 并全部通过，然后报告：改动文件清单、实现决策、测试结果。严格遵守 design.md 第 10 节 D1–D10，不要引入任何超出 M1 范围的功能。

**并行原则**：同一里程碑只派一个实施会话；需要并行时按包目录划分所有权（如 `session`/`store` 与 `kb` 分属不同会话），各会话只写自己负责的目录。

**防跑偏红线**：实施会话的报告不作为验收依据；越界功能（超出里程碑范围）一律退回，不合并。

## 9. 参考链接

### 文档

- 设计基线：[`docs/design.md`](docs/design.md)
- 原型架构：[`../deepseek-harness/docs/architecture.md`](../deepseek-harness/docs/architecture.md)
- dsh 循环细节：[`../deepseek-harness/docs/subsystems/core.md`](../deepseek-harness/docs/subsystems/core.md)
- dsh 会话日志：[`../deepseek-harness/docs/subsystems/session.md`](../deepseek-harness/docs/subsystems/session.md)
- dsh 能力接缝：[`../deepseek-harness/docs/capability-seams.md`](../deepseek-harness/docs/capability-seams.md)
- M4 参照插件（知识库，FTS5 + 提取回写）：[`../dsh-knowledge/`](../dsh-knowledge/)（[GitHub](https://github.com/lemoncat7/dsh-knowledge)）+ 调研 [`docs/research-m4-kb.md`](docs/research-m4-kb.md)
- M5 参照四个能力族：[`../deepseek-harness/packages/jobs/`](../deepseek-harness/packages/jobs/)、[`../deepseek-harness/packages/subagent/`](../deepseek-harness/packages/subagent/)、[`../deepseek-harness/packages/compaction/`](../deepseek-harness/packages/compaction/)、[`../deepseek-harness/packages/skill/`](../deepseek-harness/packages/skill/) + 子系统文档 [`docs/subsystems/{jobs,subagent,compaction,skills}.md`](../deepseek-harness/docs/subsystems/jobs.md)；M5 主 ADR `docs/decisions/2026-08-18-m5-agent-core.md`
- M6 参照六个能力族：[`../deepseek-harness/packages/schedule/`](../deepseek-harness/packages/schedule/)、[`goal/`](../deepseek-harness/packages/goal/)、[`plan/`](../deepseek-harness/packages/plan/)、[`todo/`](../deepseek-harness/packages/todo/)、[`spill/`](../deepseek-harness/packages/spill/)、[`interaction/`](../deepseek-harness/packages/interaction/)、[`code-runtime/`](../deepseek-harness/packages/code-runtime/)、[`mcp/`](../deepseek-harness/packages/mcp/)、[`fs/`](../deepseek-harness/packages/fs/)；M6 主 ADR `docs/decisions/2026-08-19-m6-agent-full.md`

### 源码参考（`../deepseek-harness/packages/`）

实现每个模块前先读对应源码，借鉴结构、接口划分与边界设计；dsh 是 TypeScript + Cordis 插件框架，**只借鉴思路，不照搬代码**。

| 本模块 | dsh 参考源码 | 重点看什么 |
|---|---|---|
| `loop` | `core/agent-loop/` | 循环驱动、turn/step 状态机 |
| `session` | `core/session/` | 事件日志、历史派生（deriveMessages） |
| `tools` | `core/tools/` | 工具注册表、参数校验、执行管道 |
| `prompt` | `core/system-prompt/` | 提示词分节组装 |
| `llm`（M8） | `llm/llm/` + `llm/llm-deepseek/` + `llm/llm-pi-ai/` + `llm/llm-retry/` | 适配器接口、流式、DeepSeek 实现、可配置路由 provider、重试包装；多模态 content parts（`7078918` 范式：落库只存 ImageAttachmentRef、请求时转 data URL） |
| `store`（M2） | `session/session-persistence*` | 持久化与重放 |
| `kb`（M4） | `../dsh-knowledge/src/`（domain/local-provider/retrieval/extraction/tools/recall）+ `web/`（seam 三件套模板） | 知识条目模型、FTS5 检索 + 中文二元组兜底、提取回写、能力接缝的包划分 |
| `jobs`（M5a） | `../deepseek-harness/packages/jobs/{jobs,jobs-local,tool-jobs}/` | owner-fenced 后台任务注册表、生命周期契约、模型侧控制工具 |
| `subagent`（M5b） | `../deepseek-harness/packages/subagent/{subagent,subagent-spawn-in-process,tool-subagent,tool-subagent-control,tool-subagent-report}/` | Provider 注册表、委托/控制/报告、子代理会话 |
| `compaction`（M5c） | `../deepseek-harness/packages/compaction/{compaction,compaction-basic,compaction-tool-result-pruner,command-compact}/` | 压缩接缝、摘要 provider、tool-result 剪枝、人工命令 |
| `skill`（M5d） | `../deepseek-harness/packages/skill/{skill,skill-filesystem,tool-skill}/` | 技能 provider 注册表、文件系统发现、目录/加载工具 |
| `schedule`（M6a） | `../deepseek-harness/packages/schedule/` | 定时调度 provider 注册表、触发语义 |
| `plan`（M6b） | `../deepseek-harness/packages/{goal,plan,todo}/` | goal→plan→todo 规划模型、推进工具 |
| `spill`（M6c） | `../deepseek-harness/packages/spill/` | 跨会话记忆、自动沉淀/召回 |
| `interact`（M6d） | `../deepseek-harness/packages/interaction/` | 审批请求/响应交互 |
| `code`（M6e） | `../deepseek-harness/packages/{code-runtime,e2b}/` | 沙箱 provider 接口、代码执行 |
| `mcp`/`fs`（M6f） | `../deepseek-harness/packages/{mcp,fs,workspace}/` | MCP 客户端、文件/工作区封装 |
| `web`（M7 候选） | `../deepseek-harness/packages/web/{web,web-search-deepseek,web-fetch-http,tool-web}/` | web 接缝三件套、DeepSeek 搜索 provider（Anthropic 兼容 Messages API + `web_search_20250305`）、fetch-http provider、`web_search`/`web_fetch` 工具与多查询扇出 |
| `terminals`/persistent shell（M9 候选） | `../deepseek-harness/packages/shell/{tool-pwsh-persistent,tool-bash-persistent}/` + `packages/terminal/{terminal,terminal-bash,tool-terminal}/` | owner-scoped 持久 shell（`ctx.terminals`）、状态保留/超时重置/输出上限、Windows ConPTY 与回显/信号限制 |
| `webServer`/Web 管理台（M10 候选） | `../dsh-knowledge/src/web.ts` + `../dsh-knowledge/web/`（静态管理台）+ `../dsh-knowledge/src/{management-proxy,api,connection,service-settings}.ts` + dsh `packages/host/webserver/` | 静态资源服务 + JSON API 路由 + bearer 认证（SHA-256 摘要）、知识库三栏文档界面、条目维护/候选审核/令牌管理、认证 HTTP API 形态 |

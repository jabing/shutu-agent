# M4 实施派发消息（控制会话 → 实施会话）

> 状态：**已按 dsh-knowledge 路线重写** 2026-08-18（控制会话已下载 `D:\dev-projects\Agent\dsh-knowledge` 并实测验证方案）· 用法：把下文整段粘贴给新开的实施会话。

---

请阅读 `D:\dev-projects\Agent\personal-agent\Agent.md`、`docs/design.md`（§8 已定稿）**和 `docs/research-m4-kb.md`（M4 调研，选型前必读），并通读参考源码 `D:\dev-projects\Agent\dsh-knowledge\`（重点：`src/domain.ts`、`src/local-provider.ts`、`src/retrieval.ts`、`src/extraction.ts`、`src/tools.ts`、`src/recall.ts`、`docs/architecture.md`）**，按设计基线实现 **M4 知识库**（里程碑验收标准见 Agent.md 第 4 节，能力结构见 design.md §8）。

**M4 范围（dsh-knowledge 路线，非向量 RAG）**：

1. **kb 服务接口（seam 的 Service 定义，internal/kb/service）**：
   ```go
   type KB interface {
       Search(ctx, query string, opts SearchOpts) ([]Hit, error)   // FTS5 + 中文二元组 LIKE 兜底
       Add(ctx, draft Entry) error                                 // 显式写入一条知识条目
       Recall(ctx, query string, limit int) ([]Hit, error)         // 主动召回（有界摘要）
       Extract(ctx, session SessionRef, turn int) error            // 回答后提取回写
   }
   type Entry struct { ID, Title, Body, Type string; Tags []string; Scope, Source string; Confidence float64; Version int }
   type Hit struct { Entry Entry; Score float64 }
   ```
   消费方（工具 / cmd/pa）只依赖接口（D2/D9）。任何 Provider 实现同一接口。

2. **SQLite Provider（本地默认，可换）**：用项目现有 `modernc.org/sqlite`（**当前钉死 v1.38.0 已内置 FTS5，勿升级依赖，勿引入新依赖**）实现：
   - 表：`knowledge_entries`（条目 + source/type/tags/scope/confidence/version）、`knowledge_fts`（FTS5 虚拟表，`tokenize='unicode61 remove_diacritics 2'`）、`extraction_jobs`（幂等，主键 = `session:turn`）。WAL、外键、事务（参考 `../dsh-knowledge/src/local-provider.ts`）。
   - 检索实现：`toFtsQuery`（空格切词加引号 OR 连接）+ FTS5 BM25 排序（权重参考 `bm25(knowledge_fts, 0.0, 4.0, 1.0, 0.5)`）+ **`fallbackTerms` 中文二元组 LIKE 兜底**（参考 `src/local-provider.ts` 的 `searchByTerms`/`fallbackTerms`/`toFtsQuery`，Go 移植，测试见下）。命中转分数 `1/(1+rank)`。
   - 索引/数据落 `data/kb/`（默认 `data/kb/knowledge.sqlite`，不入 git）。

3. **提取回写（Extract）**：每轮回答结束后由 `cmd/pa`（组合根）调用 `kb.Extract(session, turn)`（**不改 loop 结构，D4**）：
   - `extraction_jobs` 幂等认领 `session:turn`；重放不重复写。
   - 取本轮用户输入 + 最终回答，先 `Search` 既有条目作上下文，调**当前模型**（复用现有 `internal/llm`）输出**严格 JSON 候选**（type/title/body/tags，可含"跳过"）：运行时校验拒绝未知 type、越权字段、非 JSON（fail-closed）；只收明确陈述或已验证的长期知识（参考 `src/extraction.ts` + `docs/architecture.md` 提取流程；M4 裁剪掉挂载/候选审核/远程，仅直写）。
   - 失败 fail-open：**绝不影响下一轮回答**；失败/跳过以 `kb/extract` 事件记录（含 reason）。
   - 提取模型默认跟随当前会话模型；配置可覆盖（M4 可简化为仅跟随，ADR 说明取舍）。

4. **工具 Consumer（注册进 tools，默认关闭，D10）**：
   - `kb_search(query, limit)` → 条目片段 + 来源 + score（只读）；
   - `kb_read(id)` → 完整条目（只读）；
   - `kb_add(title, body, type, tags)` → 显式写入（写，`source="manual"`）。
   - 三个工具随 `kb.enabled` 自动加入白名单（沿用 M3 run_command 单一开关模式），**默认关闭**。

5. **召回与注入**：`cmd/pa` 在每轮开始时按用户输入调 `kb.Recall`（有界条数，默认 3）注入上下文；会话开始时把知识库轻量目录（名称+描述，不塞正文）注入系统提示词（`prompt` 分节，参考 `src/recall.ts`）。检索失败 fail-open。

6. **事件落日志（D3 硬要求）**：新增事件类型 `kb/recall`（主动召回注入：query+注入片段）、`kb/extract`（提取结果：created/skipped/failed+reason）、`kb/add`（显式写入：条目摘要）。`kb_search`/`kb_read` 结果走 `tool/result`（模型实际看到，已满足 D3）。历史仍是日志派生值（D1）。**不改 loop 的 turn/step 结构**（D4）——事件由 kb 自身或 cmd/pa 追加到 session 日志，loop 只负责已存在的流程。

7. **CLI**：`/kb-status`（条目数/库大小/最近提取）、`/kb-reindex`（重建 FTS 索引）。`kb.enabled` 决定 kb 服务/工具是否注册。

8. **config**（`internal/config` 扩展）：
   ```yaml
   kb:
     enabled: false
     db_path: data/kb/knowledge.sqlite
     top_k: 5
     recall_limit: 3          # 每轮主动召回条数；0 = 关闭主动召回
     extraction: true         # 回答后提取回写开关
     catalog: true            # 系统提示词注入轻量目录开关
   ```

**决策记录（必交）**：写 `docs/decisions/2026-08-18-m4-kb-architecture.md`，至少覆盖三件事：① **检索方案** = FTS5 + 中文二元组 LIKE 兜底（依据：dsh-knowledge 实测 + Go 移植实测，零依赖，中文/英文/混合全过；说明为何不用向量/embedding）；② **提取回写机制**（幂等、严格 JSON fail-closed、不阻断回答、提取模型选择；对照 dsh-knowledge 的裁剪取舍）；③ **kb/recall、kb/extract、kb/add 落日志机制**（如何在不改 loop 结构的前提下满足 D3）。

**约束**（严格遵守 design.md 第 10 节 D1–D10）：

- 不改 loop 的 turn/step 结构（D4）；kb 是能力接缝，Service/Provider/Tool 三件套齐全（D2/D9）；检索后端与对话模型解耦（D9）。
- **明确不做**：embedding/向量检索、文档解析/分块摄入（kb_add 只收纯文本/正文，不做 Markdown 解析器）、PDF/OCR/网页抓取、Web UI、多知识库/挂载/标签权限矩阵、候选审核/远程 API、自动注入完整 RAG（M4 只做有界 recall + 模型驱动的 kb_search）。
- kb 工具与提取默认关闭（D10）；`kb_search`/`kb_read` 只读、`kb_add`/`Extract` 写，沿用 M3 白名单门。
- 保持 CGO-free；**不新增任何第三方依赖**（FTS5 在现有 v1.38.0 内）；Go 沙箱绕行沿用项目内缓存（`.gomodcache` / `.gocache` / `.gopath`）。
- 原有测试必须保持绿色；知识库数据落 `data/kb/`（不入库）。

**参考源码**：`D:\dev-projects\Agent\dsh-knowledge\`（同生态知识库插件，重点文件见开头；**只借鉴思路与数据模型，不照搬 TS 代码**）。架构原则参考 `D:\dev-projects\Agent\deepseek-harness\docs\architecture.md`、`docs\capability-seams.md`。

**自测（全部通过后提交，提交信息含 M4）**：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：中文检索（单字/词/短语，走二元组 LIKE 兜底）、英文检索（走 FTS5 BM25）、混合检索、`kb_add` 后能检索到、`kb_read` 返回完整条目、**换 Provider 消费方代码不变**（同一工具/服务代码对两个 Provider 跑通，验证接口边界）、Extract 幂等（同一 session:turn 不重复写）、提取严格 JSON fail-closed（坏输出 → failed 事件不崩）、`kb/recall`/`kb/extract`/`kb/add` 事件落日志、kb 默认关闭（未启用时工具不注册）、fail-open（检索/提取失败不阻断回答）。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash、ADR 路径。

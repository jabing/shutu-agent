# M4a 实施派发消息（控制会话 → 实施会话）——知识库内核

> 状态：已派发 2026-08-18（M4 拆三段：M4a 内核 → M4b 工具与召回 → M4c 提取回写；本文件为第一段）· 用法：把下文整段粘贴给新开的实施会话。

---

请阅读 `D:\dev-projects\Agent\personal-agent\Agent.md`、`docs/design.md`（§8 已定稿）**和 `docs/research-m4-kb.md`（M4 调研，选型前必读），并通读参考源码 `D:\dev-projects\Agent\dsh-knowledge\`（重点：`src/domain.ts`、`src/local-provider.ts`、`src/retrieval.ts`、`docs/architecture.md`）**，按设计基线实现 **M4a 知识库内核**（M4 第一段；完整 M4 验收标准见 Agent.md 第 4 节，本段验收标准见下）。

**M4a 范围（只做内核，不碰工具/提取/召回编排）**：

1. **kb 服务接口（seam 的 Service 定义，internal/kb/service）**：
   ```go
   type KB interface {
       Search(ctx, query string, opts SearchOpts) ([]Hit, error)   // FTS5 + 中文二元组 LIKE 兜底
       Add(ctx, draft Entry) error                                 // 显式写入一条知识条目
       Recall(ctx, query string, limit int) ([]Hit, error)         // 主动召回（有界摘要，本段实现检索逻辑，编排在 M4b）
       // Extract 在 M4c 加（本段接口预留，勿实现）
   }
   type Entry struct { ID, Title, Body, Type string; Tags []string; Scope, Source string; Confidence float64; Version int }
   type Hit struct { Entry Entry; Score float64 }
   type SearchOpts struct { TopK int; Scope string }
   ```
   消费方只依赖接口（D2/D9）。

2. **SQLite Provider（本地默认，可换）**：用项目现有 `modernc.org/sqlite`（**当前钉死 v1.38.0 已内置 FTS5，勿升级依赖，勿引入新依赖**）实现：
   - 表：`knowledge_entries`（条目 + source/type/tags/scope/confidence/version）、`knowledge_fts`（FTS5 虚拟表，`tokenize='unicode61 remove_diacritics 2'`）。WAL、外键、事务（参考 `../dsh-knowledge/src/local-provider.ts`）。
   - 检索实现：`toFtsQuery`（空格切词加引号 OR 连接）+ FTS5 BM25 排序（权重参考 `bm25(knowledge_fts, 0.0, 4.0, 1.0, 0.5)`）+ **`fallbackTerms` 中文二元组 LIKE 兜底**（参考 `src/local-provider.ts` 的 `searchByTerms`/`fallbackTerms`/`toFtsQuery`，Go 移植）。命中转分数 `1/(1+rank)`。
   - `Search` 先走 FTS5，不足 topK 时用二元组 LIKE 兜底补充（参考 `src/local-provider.ts` search 的补充逻辑）。
   - 数据落 `data/kb/`（默认 `data/kb/knowledge.sqlite`，不入 git）。
   - `Add` 写入条目并同步 FTS 索引；同 source 更新走 `version+1`（更新历史可简化：M4a 只记当前版本 + updated 时间，版本历史表留 M4c/M5 评估）。

3. **config（internal/config 扩展，仅本段所需字段）**：
   ```yaml
   kb:
     enabled: false
     db_path: data/kb/knowledge.sqlite
     top_k: 5
   ```
   （`recall_limit`/`extraction`/`catalog` 在 M4b/M4c 加，本段不加。）

**决策记录（必交）**：写 `docs/decisions/2026-08-18-m4-kb-architecture.md`（M4 主 ADR），本段至少覆盖：① **检索方案** = FTS5 + 中文二元组 LIKE 兜底（依据：dsh-knowledge 实测 + Go 移植实测，零依赖，中文/英文/混合全过；说明为何不用向量/embedding）；② Provider 抽象与接口边界（如何满足"换 Provider 不改消费方"）；③ 事件落日志机制（`kb/recall` 事件类型如何在不改 loop 结构的前提下满足 D3，D3 机制在 M4a 就位）。M4c 的提取回写决策后续补写进同一 ADR。

**约束**（严格遵守 design.md 第 10 节 D1–D10）：

- 不改 loop 的 turn/step 结构（D4）；kb 是能力接缝，Service/Provider 两件套本段齐全（Tool 在 M4b）（D2/D9）。
- **明确不做（本段）**：工具（`kb_search`/`kb_read`/`kb_add`）、提取回写 Extract、召回编排/目录注入、CLI（/kb-status /kb-reindex）、多知识库/挂载/标签权限矩阵、embedding/向量、文档解析。只实现接口 + Provider + config + 事件类型声明。
- `kb.enabled` 默认关闭（D10）。
- 保持 CGO-free；**不新增任何第三方依赖**（FTS5 在现有 v1.38.0 内）；Go 沙箱绕行沿用项目内缓存（`.gomodcache` / `.gocache` / `.gopath`）。
- 原有测试必须保持绿色；知识库数据落 `data/kb/`（不入库）。

**参考源码**：`D:\dev-projects\Agent\dsh-knowledge\`（同生态知识库插件，重点文件见开头；**只借鉴思路与数据模型，不照搬 TS 代码**）。架构原则参考 `D:\dev-projects\Agent\deepseek-harness\docs\architecture.md`、`docs\capability-seams.md`。

**自测（全部通过后提交，提交信息含 M4a）**：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：中文检索（单字/词/短语，走二元组 LIKE 兜底）、英文检索（走 FTS5 BM25）、混合检索、`Add` 后能检索到、同 source 更新版本递增、**换 Provider 消费方代码不变**（同一服务代码对两个 Provider 跑通，验证接口边界）、`kb/recall` 事件类型可落日志（类型声明 + 追加路径测试）、kb 默认关闭（enabled=false 时不初始化）。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash、ADR 路径。

# ADR: M4 知识库架构——FTS5 + 中文二元组 LIKE 兜底检索，Provider 抽象，kb/recall 事件落日志

- 状态：**已定**（2026-08-18，M4a 内核落地）
- 关联：design.md §8、§10 D1–D10（重点 D2/D3/D9/D10）；Agent.md 路线图 M4；调研 `docs/research-m4-kb.md`；参考实现 `../dsh-knowledge/`
- 分阶段：本 ADR 是 M4 主 ADR。本段（M4a）定稿三件事：① 检索方案 ② Provider 抽象与接口边界 ③ 事件落日志机制。M4c 的提取回写决策后续补写进同一 ADR。

## 背景

M4 要给个人 Agent 接入知识库能力（设计基线 design.md §8，参照 dsh-knowledge：FTS5 全文检索 + 提取回写，非向量 RAG）。M4a 是第一段——只做内核（Service 接口 + SQLite Provider + config + 事件类型声明），不碰工具/提取/召回编排（M4b/M4c）。

实现前必须回答三个问题：

1. 检索怎么做？个人规模、本地离线、中文友好的全文检索；
2. 怎么让"换后端不换消费方"成立（design.md D2/D9 的能力接缝）？
3. 模型主动召回注入是**新的模型可见输入**，怎么满足 D3（模型可见 ⇒ 已落日志）而不改 loop 结构（D4）？

## 决策 ① 检索方案：SQLite FTS5 + 中文二元组 LIKE 兜底

**用 modernc.org/sqlite v1.38.0 自带 FTS5 虚拟表做 BM25 全文检索，中文二元组 LIKE 兜底补充，不引入向量/embedding。**

具体（对应 `internal/kb/`）：

- 表：`knowledge_entries`（条目 + source/type/tags/scope/confidence/version/created_at/updated_at）+ `knowledge_fts`（FTS5，`tokenize = 'unicode61 remove_diacritics 2'`，列为 knowledge_id UNINDEXED / title / body / tags）。WAL、外键、busy_timeout、`BEGIN` 事务（`Add` 条目 + FTS 同步同事务）。
- 检索（`Search`）：
  1. `toFtsQuery`：空格切词、逐词加引号（内嵌引号双写）、OR 连接、上限 20 词；
  2. FTS5 BM25 排序 `bm25(knowledge_fts, 0.0, 4.0, 1.0, 0.5)`（四列权重依序：knowledge_id/title/body/tags，标题权重最高）；MATCH 语法错误按"无 FTS 命中"处理，交给兜底；
  3. FTS 结果不足 topK 时，`fallbackTerms` 兜底：英文词原样（小写）、连续中文切相邻二元组（单字串保留单字），对 title/body/tags 做 `LIKE '%xx%'`（转义 `\ % _`）OR 连接，`updated_at DESC` 排序，去重补足 topK；
  4. 命中转分数 `1/(1+max(0,rank))`；兜底行沿用 dsh-knowledge 的固定 rank=3.0（分数恒低于真实 FTS 命中）。
- `Recall` = 有界检索（limit 即界），注入编排在 M4b。
- `Add`：同 source 更新走 `version+1`（更新历史表留 M4c/M5 评估，M4a 只记当前版本 + updated 时间）；否则新条目 version=1。
- 数据落 `<data_dir>/kb/knowledge.sqlite`（`data/` 已在 .gitignore，不入库）。

**理由（为何不用向量/embedding）：**

1. **零新依赖 + CGO-free**：项目硬约束（design.md §9），向量库/嵌入模型客户端全是新依赖；FTS5 在已钉死的 v1.38.0 内即内置启用（`SQLITE_ENABLE_FTS5=1`），本次实现**零依赖升级、零新依赖**。
2. **实测可用**：`docs/research-m4-kb.md` §Go 实测——toFtsQuery + fallbackTerms 完整移植后，中文"架构/中文/知识库"、英文"sqlite"、混合"FTS5 提取"全部正确命中；M4a 的 `TestProviderSwapConsumerUnchanged` 中文/英文/混合断言全绿（走的就是这一条路径）。
3. **个人规模**：本地个人知识条目数量级远不需要语义向量索引；dsh-knowledge 本身就不用向量，与"薄核心 + 日志即事实"同频。
4. **FTS5 中文缺陷有既定兜底**：unicode61 把连续中文当单个 token（"架构决策记录"整体一词），搜"架构"不中；trigram 需 ≥3 字（两字失效）。二元组 LIKE 兜底精准补齐（单字/词/短语/混合全过）。
5. **接口已预留演进**：向量/embedding 仅作 M5 可选 Provider（design.md §8 明确推迟），由 Provider 抽象兜底，切换成本≈0。

## 决策 ② Provider 抽象与接口边界（换 Provider 不改消费方）

**知识库是能力接缝（D2/D9）：`internal/kb` 的 `KB` 接口是 Service，消费方（工具、召回编排）只依赖接口；后端实现（Provider，可换）满足同一接口。**

- 接口（`internal/kb/service.go`）：

  ```go
  type KB interface {
      Search(ctx context.Context, query string, opts SearchOpts) ([]Hit, error)
      Add(ctx context.Context, draft Entry) error
      Recall(ctx context.Context, query string, limit int) ([]Hit, error)
      Close() error
      // Extract 在 M4c 加（接口预留，勿实现）
  }
  type Entry struct{ ID, Title, Body, Type string; Tags []string; Scope, Source string; Confidence float64; Version int }
  type Hit struct{ Entry Entry; Score float64 }
  type SearchOpts struct{ TopK int; Scope string }
  ```

  `Close` 属于接口：Provider 持有外部资源（DB 句柄），随 Provider 生命周期释放，避免换后端泄漏。`Extract` 按派发要求**不在本段声明**（声明即强迫所有 Provider 出桩实现，留 M4c）。
- 本段两个 Provider：**默认 SQLite**（`internal/kb/sqlite.go`，FTS5 + 二元组兜底）+ **内存**（`internal/kb/mem.go`，简化词包含匹配）。
- 共享语义集中在 seam 包：`normalizeDraft`（标题 1–200、正文 1–50000、type ∈ {preference,fact,decision,procedure,lesson}、confidence∈[0,1]、tags 规范化）、`toFtsQuery`/`fallbackTerms`/`rankToScore`/`escapeLike`/`normalizeTopK`。所有 Provider 复用同一套检索与校验，跨后端行为一致。
- **边界验收**（`TestProviderSwapConsumerUnchanged`）：同一份消费方代码（增删查 + 版本 + scope + topK 断言）对 SQLite 与内存 Provider 全绿——证明消费方只依赖接口，换后端零改动。
- 数据目录归 config：`kb.db_path` 空值默认 `<data_dir>/kb/knowledge.sqlite`（跟随 data_dir），显式值原样使用。

## 决策 ③ 事件落日志机制（kb/recall 满足 D3，不改 loop）

**主动召回把知识注入模型上下文是新的"模型可见输入"，必须在注入前落日志（D3）。机制：在会话日志词汇表新增 `kb/recall` 事件类型，由组合根（M4b 的召回编排）在循环外调用 `session.NewKBRecall` 追加，loop 的 turn/step 结构零改动（D4）。**

- `internal/session/session.go`：新增常量 `EventKBRecall = "kb/recall"` + 载荷构造 `NewKBRecall(query string, hits []session.RecallHit)`（`RecallHit`：id/title/snippet/type/tags/scope/source/score——"有界摘要"的纯数据投影，`session` 不依赖 `kb` 包）。
- **D3 在 M4a 就位**：类型声明 + 追加路径测试（`session.TestKBRecallEventAppendsAndReplays`：sink 追加 → JSON 往返 → 重启重放；`store.TestKBRecallEventPersistsAndReplays`：log sink → SQLiteStore → 回放）。
- `DeriveHistory` 对 `kb/recall` 视为不透明数据（不回放成消息）：召回由编排方直接注入上下文，日志只负责"已注入"的事实记录（设计基线 §8：检索失败 fail-open，不阻断回答）。
- 其余 kb 事件（`kb/extract`、`kb/add`）随 M4b/M4c 依序加入，同一机制，事件带 `Version` 字段（D8）兼容演进。

## 决策 ④ kb 默认关闭（D10）

`kb.enabled: false` 为默认。`internal/kb.NewFromConfig` 是唯一构造入口：`enabled=false` 时返回 `(nil, nil)` 且**不打开任何数据库文件**（`TestNewFromConfigDisabled` + config 默认测试覆盖）。M4b 在组合根（cmd/pa）用它接线，接缝当前只交付接口 + Provider + config + 事件类型。

## 后果

### 放弃的方案

- **向量检索 / embedding**（向量库、HNSW、嵌入模型客户端）：违背零新依赖 + CGO-free；个人规模无需语义索引；M5 如需再评估（design.md §8 已留接口）。
- **FTS5 trigram tokenizer**：需 ≥3 字查询，"架构"等两字词失效，中文不友好。
- **纯 LIKE / 纯 FTS5**：纯 LIKE 无 BM25 排序、英文无词干化；纯 FTS5 中文单 token 缺陷命中不了。二者互补才满足中文/英文/混合。

### 残余风险与后果

- **二元组兜底是启发式**：过短的查询（如"知识"）因二元组命中多条属预期；匹配无权重、无语义（"来源"会把不相干条目带进来）。个人规模可接受，M5 若引入向量可替换 Provider 解决。
- **同 source 更新只记当前版本**：版本历史表（dsh-knowledge `knowledge_versions`）留 M4c/M5 评估；M4a 满足"只记当前版本 + updated 时间"。
- **内存 Provider 是简化实现**（词包含匹配），仅用于验证接口边界与作参考实现，非生产后端；生产默认 SQLite。
- **`kb/recall` 载荷含 snippet**：正文摘要的有界性由 M4b 的注入格式化保证；事件只记录注入前的那一刻，避免日志膨胀。

### 何时可重评

M5 出现子代理、多租户或共享知识库需求时，评估向量/远程 Provider（一次接口新增，消费方不变）；提取回写（M4c）决策后续补写进本 ADR。

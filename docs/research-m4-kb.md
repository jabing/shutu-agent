# M4 知识库开源调研（控制会话 · 2026-08-18）

> 用途：为 M4 实现前的技术选型提供依据。结论优先，细节在后。
> 设计基线仍在 design.md §8（已按 dsh-knowledge 方向定稿）。本文件是"一个事实一个家"的调研归属地。

## 结论（推荐，已定稿）

1. **首选参考项目 = [dsh-knowledge](https://github.com/lemoncat7/dsh-knowledge)**（2026-08-18 经用户指定并下载到 `../dsh-knowledge/`）。它是最贴近我们架构的近亲：同样是 DeepSeek Harness 生态、同源的"能力接缝 + 日志即事实"理念。**它是知识库插件，不是向量 RAG 平台**——核心思路与我们 v1 的"薄核心"完全同频。
2. **放弃向量检索，采用 dsh-knowledge 的技术路线**：
   - **检索 = SQLite FTS5 全文检索**（`unicode61 remove_diacritics 2` tokenizer）+ **中文二元组 LIKE 兜底**；
   - **知识来源 = 回答后模型提取回写**（LLM 判断本轮是否产生可复用知识并写入条目）+ 显式 `kb_add`；
   - **无需 embedding、无需向量库、无需本地嵌入模型**——完全离线、零新依赖。
3. **该方案已由控制会话在 Go/modernc 栈实测验证通过**（见下方 §Go 实测）：FTS5 在项目当前钉死的 `modernc.org/sqlite v1.38.0` 上即已内置启用（`SQLITE_ENABLE_FTS5=1`），中文/英文/混合查询全部正确命中。**零依赖升级、零新依赖**。

## 为什么是 dsh-knowledge（而非此前调研的 RAG 平台）

| 维度 | RAGFlow / Dify / AnythingLLM / khoj / FastGPT | dsh-knowledge |
|---|---|---|
| 形态 | Python/Node 重平台或桌面应用 | 轻量插件，单进程模块化单体内聚 |
| 检索 | 向量（embedding）+ 可选的全文/混合 | **纯 FTS5 全文 + 中文二元组兜底，无向量** |
| 知识来源 | 文档摄入（解析→分块→嵌入） | **对话提取回写 + 显式写入**（不解析文档） |
| 与我们约束 | 冲突（Go 单二进制 / CGO-free / 本地优先） | 完全兼容（Go 用 modernc sqlite 自带 FTS5） |
| 借鉴方式 | 只借架构流程，不照搬代码 | 同生态，**可整体参照其设计与数据模型** |

> 注：此前的"经典 RAG 流水线"结论（加载→分块→嵌入→索引→检索→注入）对**文档型**知识库仍成立，但 M4 的目标定位已从"索引个人笔记目录"调整为"**对话沉淀 + 显式写入的可检索个人知识**"（dsh-knowledge 模式）。文档解析/向量检索留给 M5 作可选 Provider（design.md §8 已预留接口），不进入 M4。

## dsh-knowledge 架构要点（M4 参照来源，源码 `../dsh-knowledge/`）

- **数据模型**（`src/domain.ts`）：知识条目 `{title, body, type, tags, scope, confidence, version, source}`；`type ∈ {preference, fact, decision, procedure, lesson}`；条目有版本历史；`source` 记录会话/轮次来源。
- **存储**（`src/local-provider.ts`）：SQLite（WAL、外键、`BEGIN IMMEDIATE` 事务）；`knowledge_entries` + `knowledge_fts`（FTS5 虚拟表，`unicode61 remove_diacritics 2`）+ `extraction_jobs`（幂等任务表，`source_key` 主键）+ 挂载/候选/令牌表（M4 裁剪）。
- **检索**（`src/local-provider.ts` search / `src/retrieval.ts`）：
  - FTS5 查询构造 `toFtsQuery`：按空格切词，逐词加引号，`OR` 连接；
  - BM25 排序：`bm25(knowledge_fts, 0.0, 4.0, 1.0, 0.5)`（title 权重高）；
  - **中文兜底 `fallbackTerms`**：FTS 结果不足时，把查询里的中文串切成相邻二元组（`架构` → `架构`），对 title/body/tags 做 `LIKE '%xx%'` 补充；
  - 命中转分数 `rankToScore = 1/(1+rank)`。
- **提取回写**（`src/extraction.ts`）：每轮回答结束后，`claimExtraction(sessionId:turn)` 幂等认领任务 → 检索既有条目作上下文 → LLM 输出严格 JSON 候选（拒绝未知目标/类型/越权字段，fail-closed）→ 按挂载的写模式入候选（audit）或直写（direct）→ 冲突/重复/合并策略。提取模型默认沿用当前会话模型。
- **召回与工具**（`src/recall.ts` / `src/tools.ts`）：`agent/pre-step` 主动召回有界条数（默认 3）注入上下文；只读工具 `knowledge_search` / `knowledge_read`（分页、签名的会话内句柄）；`knowledge_base_create/update` 显式管理工具。
- **提示词注入**：`system-prompt/assemble` 把挂载库的**名称+描述**作为轻量目录注入（不塞正文），让模型知道何时该调 `knowledge_search`。
- **安全边界**（M4 裁剪，保留精神）：检索失败 fail-open（不阻断回答）；提取拒绝秘密与临时输出；条目体/标题有长度上限。

## Go 实测（控制会话 2026-08-18 执行，`_scratch/ftstest/` 已验证后清理）

1. **FTS5 可用性**：`modernc.org/sqlite v1.38.0`（项目当前钉死版本）编译时 `SQLITE_ENABLE_FTS5 = 1`，`fts5` 虚拟表 + `bm25()` + `snippet()` 全部可用，无需升级依赖。
2. **中文缺陷确认**：FTS5 默认 `unicode61` 把连续中文当**单个 token**（"架构决策记录"整体一个词），搜"架构"匹配不到；`trigram` 需 ≥3 字查询（"架构"两字失效）。
3. **dsh-knowledge 方案在 Go 移植验证**：`toFtsQuery` + `fallbackTerms` 完整移植后，中文查询"架构""中文""知识库"、英文"sqlite"、混合"FTS5 提取"**全部正确命中**；英文走 FTS5 BM25，中文走二元组 LIKE 兜底。**方案成立，直接采用。**

## 向量存储 / embedding（明确不进 M4）

| 项 | 结论 |
|---|---|
| 向量库（暴力余弦 / sqlite vec / HNSW） | M4 不需要。FTS5 全文检索零依赖覆盖个人规模；向量仅作 M5 可选 Provider 预留（design.md §8 接口已留） |
| 嵌入模型（Ollama / bge-m3 等） | M4 不需要，不引入嵌入客户端。若 M5 加语义检索，届时一个 OpenAI 兼容 embeddings 客户端可同时覆盖本地 Ollama 与远程（[Ollama /v1/embeddings PR #5470](https://github.com/ollama/ollama/pull/5470)） |

## 对 M4 派发的影响（已同步）

- 派发消息整体重写为 **dsh-knowledge 路线**：Service 接口为 `Search / Add / Recall / Extract`；Provider 为 SQLite FTS5；工具为 `kb_search / kb_read / kb_add`；回答后提取回写；`kb/recall` / `kb/extract` / `kb/add` 事件落日志。
- ADR 必须覆盖三件事：① 检索 = FTS5 + 中文二元组 LIKE 兜底（零依赖依据 + 实测）；② 提取回写机制（幂等、严格 JSON、fail-closed、不阻断回答）；③ `kb/recall`/`kb/extract`/`kb/add` 落日志机制（不改 loop 结构，D3/D4）。
- 实施会话应先读本文件 + `../dsh-knowledge/`（重点 `src/domain.ts`、`src/local-provider.ts`、`src/retrieval.ts`、`src/extraction.ts`、`src/tools.ts`、`src/recall.ts`）再做 ADR。

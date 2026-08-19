# M4 知识库开源调研（控制会话 · 2026-08-18）

> 用途：为 M4 实现前的技术选型提供依据。结论优先，细节在后。参考项目清单与 Go 向量存储选项是**外部资料调查**，本文件是"一个事实一个家"的调研归属地；设计基线仍在 design.md §8。

## 结论（推荐）

1. **不照搬任何现成 RAG 平台**。主流个人知识库工具（RAGFlow / Dify / AnythingLLM / fastGPT / Cherry Studio / khoj）都是 Python/Node 重平台或桌面应用，与我们"Go 单二进制、CGO-free、本地优先"的硬约束冲突。它们的价值在**架构与流程**，不在代码。
2. **参考架构 = 经典 RAG 流水线**（几乎所有项目一致）：`加载 → 分块 → 嵌入 → 索引 → 检索（带来源）→ 注入`。我们已在 M4 派发中固化此流水线，无需发明。
3. **向量存储：优先"纯 Go 且轻"**。个人笔记规模（几千~几万块）根本不需要分布式向量库。候选按推荐度：
   - **A. 进程内暴力余弦索引 + 持久化文件**（零新依赖）：对个人规模完全够，最简单、最可维护；
   - **B. `modernc.org/sqlite` ≥v1.56.0 内置 `vec` 包**（纯 Go sqlite-vec 移植，`vec0` 虚拟表 + SQL KNN）：复用现有 SQLite、CGO-free、可 SQL 联表查元数据——但需**升级依赖**（现钉 v1.38.0 无 vec），且是生成的低层 API，成熟度未知；
   - **C. 第三方纯 Go HNSW 库**（govector / vecgo / quiver / kektordb）：可扩展性更好，但新增依赖、生态较新。
   - 推荐默认 **A**，实施会话在 ADR 中实测评估 B 是否低代价可用；C 仅当未来规模需求出现再引入（对应 design.md §8"可换 Provider"）。
4. **嵌入 provider：一个 OpenAI 兼容 embeddings 客户端覆盖本地与远程**。Ollama 已提供 OpenAI 兼容的 `/v1/embeddings` 端点（另原生 `/api/embed`）。默认指向本地 Ollama（如 `nomic-embed-text` / `bge-m3`），远程 API 仅需改 base_url + model + key_env。这确认了 M4 派发的嵌入设计成立。

## 开源参考项目（各取所长，不照搬）

| 项目 | 语言/形态 | 值得借鉴 | 不要照搬 |
|---|---|---|---|
| [RAGFlow](https://github.com/infiniflow/ragflow) | Python 服务 | 文档解析/分块流水线、引用溯源设计 | 服务架构、依赖栈、模板 |
| [Dify](https://github.com/langgenius/dify) | Python 平台 | 知识库应用编排、检索策略（向量/全文/混合） | 平台化、多租户、前端 |
| [AnythingLLM](https://github.com/Mintplex-Labs/anything-llm) | Node + Python | 本地优先的向量库抽象与可换 provider 思路 | 桌面壳、Node 栈 |
| [khoj](https://github.com/khoj-ai/khoj) | Python | "AI 第二大脑"个人知识库定位、增量索引思路 | 服务端、插件体系 |
| [LangChain / LlamaIndex](https://github.com/run-llama/llama_index) | Python 框架 | **文档加载器/文本分块器/检索器/索引**的规范命名与边界划分 | 框架本身 |
| [FastGPT](https://github.com/labring/FastGPT) | Node + Python | 知识库问答流、命中片段排序展示 | 平台化 |

## Go 本地向量存储选项（M4 ADR 的评估输入）

| 选项 | 仓库 | 特点 | 对照约束 |
|---|---|---|---|
| A. 暴力余弦索引 | 自写 | 零依赖、O(n) 线性扫描；个人规模无感 | ✅ CGO-free、单二进制、无新依赖 |
| B. modernc sqlite `vec` | `modernc.org/sqlite` v1.56.0+ | 纯 Go sqlite-vec 移植，`vec0` 虚拟表 + `vector_distance_cosine`，可 SQL 检索与联表 | ✅ CGO-free；⚠️ 需从 v1.38.0 升级、低层生成 API |
| C. govector | [DotNetAge/govector](https://github.com/DotNetAge/govector) | "SQLite for Vectors"，纯 Go、HNSW、Qdrant 兼容 API | ✅ CGO-free；⚠️ 新依赖、生态新 |
| C. vecgo | [hupe1980/vecgo](https://github.com/hupe1980/vecgo) | 纯 Go 嵌入式混合向量库，HNSW + DiskANN | ✅ CGO-free；⚠️ 新依赖、生态新 |
| C. quiver / kektordb | pkg.go.dev 收录 | 纯 Go 向量库 | ✅ CGO-free；⚠️ 小众 |

> 注：本地首选 Ollama 时**无需远程向量库**（Qdrant/pgvector 不在 M4 范围内，属设计 §8 的"远程 Provider 备选"接口预留）。

## 嵌入模型参考

- Ollama 嵌入端点：原生 `POST /api/embed`，OpenAI 兼容 `POST /v1/embeddings`（[ollama PR #5470](https://github.com/ollama/ollama/pull/5470)）。
- 本地模型候选：`nomic-embed-text`（768 维，通用）、`bge-m3`（1024 维，多语言，中文友好）、`mxbai-embed-large`（1024 维）。中文笔记场景优先考虑 `bge-m3`。
- 维数影响索引体积与算力，个人规模下 768~1024 维皆可。

## 对 M4 派发的影响（已同步）

- 向量存储 ADR 的评估输入从"sqlite-vec vs 纯 Go 二选一"扩展为 **A/B/C 三档**，推荐 A、评估 B、C 留待未来。
- 嵌入设计维持"一个 OpenAI 兼容客户端覆盖本地/远程"，已由本次调研证实可行。
- 新增参考：本文档。实施会话应先读 `docs/research-m4-kb.md` 再做 ADR 选型。

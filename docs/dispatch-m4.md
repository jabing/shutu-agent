# M4 实施派发消息（控制会话 → 实施会话）

> 状态：已派发 2026-08-18 · 用法：把下文整段粘贴给新开的实施会话。

---

请阅读 `D:\dev-projects\Agent\personal-agent\Agent.md` 和 `docs/design.md`，按设计基线实现 **M4 知识库**（里程碑验收标准见 Agent.md 第 4 节，能力结构见 design.md §8）。

**M4 范围**：

1. **kb 服务接口（seam 的 Service 定义）**：`internal/kb/service` 定义纯接口
   ```go
   type KB interface {
       Ingest(ctx, source string, text string) error
       Retrieve(ctx, query string, topK int) ([]Chunk, error)
   }
   type Chunk struct { Text, Source string; Score float64 }
   ```
   消费方（工具）只依赖接口（D2/D9）。任何 Provider 实现同一接口。

2. **向量 Provider（本地优先，可换）**：默认实现为**纯 Go 进程内向量索引**（余弦相似度），持久化到 `data/kb/`（已 gitignore）。**必须评估 sqlite-vec 是否可用并记录结论于 ADR**：本项目硬约束 CGO-free（design.md §9），若 sqlite-vec 的 Go 绑定在 Windows 无 CGO 工具链下可用则优先，否则纯 Go 索引即为默认并写明依据。远程 Provider（pgvector / Qdrant）只留接口，M4 不必实现。

3. **嵌入 Provider（独立，与对话模型解耦，D9）**：实现**一个 OpenAI 兼容的 embeddings 客户端**（`POST {base_url}/embeddings`，入参 `{model, input: []string}`）。这一个实现同时覆盖两种场景——默认指向本地 Ollama（`http://localhost:11434/v1`，模型如 `nomic-embed-text` 或 `bge-m3`），远程 API 只需改 base_url + model。**API Key 仍只走环境变量**：config 里只写环境变量名（如 `kb.embed.key_env: EMBED_API_KEY`），空表示免鉴权（Ollama 场景）。

4. **分块（chunking）**：Markdown 感知——按标题/段落切分，块大小上限（默认 1000 字符，可配），来源 = 文件路径 + 标题。增量索引：按文件内容哈希/mtime 跳过未变文件（`/kb-reindex` 可强制重建）。

5. **工具 Consumer（注册进 tools 注册表）**：
   - `kb_search(query, topK)` → 片段 + 来源（只读）；
   - `kb_add(source, text)` → 分块入库（写）；
   - 两个工具随 `kb.enabled` 自动加入白名单（沿用 M3 run_command 的单一开关模式），**默认关闭**（D10 延续）。

6. **`kb/retrieval` 事件落日志（D3 硬要求）**：检索行为对模型可见 ⇒ 必须落结构化日志事件（query、返回片段、来源）。建议机制：扩展 `tools.Result` 增加可选的结构化载荷字段，loop 在 `tool/result` 之后**无条件附带追加**该载荷（loop 仍是通用的，不知道 kb 概念，turn/step 结构零改动）。也可在 ADR 中论证其他**不改 loop 结构**的方案。

7. **CLI**：`/kb-status`（已索引块数/来源数）、`/kb-reindex`（重建索引）。索引目录由 `config kb.sources`（目录列表）指定，启动时增量索引。

8. **config**（`internal/config` 扩展）：
   ```yaml
   kb:
     enabled: false
     sources: []          # 个人笔记目录列表
     top_k: 5
     chunk_size: 1000
     embed:
       base_url: http://localhost:11434/v1   # Ollama 默认
       model: nomic-embed-text
       key_env: ""        # 环境变量名；空 = 免鉴权（本地）
   ```

**决策记录（必交）**：写 `docs/decisions/2026-08-18-m4-kb-architecture.md`，至少覆盖三件事：① 向量存储选型结论（sqlite-vec vs 纯 Go，对照 CGO-free 硬约束给出实测或明确依据）；② 嵌入 provider 默认（本地 Ollama 优先、OpenAI 兼容覆盖远程，为何一个实现够用）；③ `kb/retrieval` 落日志机制（如何在不改 loop 结构的前提下满足 D3）。

**约束**（严格遵守 design.md 第 10 节 D1–D10）：

- 不改 loop 的 turn/step 结构（D4）；kb 是能力接缝，Service/Provider/Tool 三件套齐全（D2/D9）；嵌入与对话模型解耦（D9）。
- 检索结果必落日志（D3）：`tool/result`（模型实际看到的文本）+ `kb/retrieval`（结构化）。历史仍是日志派生值（D1）。
- **明确不做**：全文/关键词混合检索、PDF/OCR/网页抓取（仅 Markdown/纯文本）、Web UI、自动注入 RAG（M4 只做模型驱动的 `kb_search`；knowledge 分节注入是未来项）、远程向量库实装。
- kb 工具默认关闭（D10）；`kb_search` 只读、`kb_add` 写，沿用 M3 白名单门。
- 保持 CGO-free；Go 沙箱绕行沿用项目内缓存（`.gomodcache` / `.gocache` / `.gopath`）。
- 原有测试必须保持绿色；索引数据落 `data/kb/`（不入库）。

**参考源码**：dsh 没有 RAG/知识库实现，设计以 design.md §8 为准。参考结构模板：`D:\dev-projects\Agent\deepseek-harness\packages\web\`（能力三件套：Service Definition + Provider 注册 + tool Consumer 的包划分）、`packages\core\tools\`（工具 schema 与执行管道）。只借鉴思路，不照搬代码。

**自测（全部通过后提交，提交信息含 M4）**：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：检索返回正确片段+来源（mock 嵌入 + 临时目录 Markdown 样本）、**换 Provider 消费方代码不变**（同一工具代码对两个 Provider 跑通，验证接口边界）、分块（标题感知 + 上限）、`kb/retrieval` 事件落日志、`kb_search` 结果含来源、增量索引跳过未变文件、kb 默认关闭。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash、ADR 路径。

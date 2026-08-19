# M4b 实施派发消息（控制会话 → 实施会话）——工具与召回注入

> 状态：已派发 2026-08-18（M4 拆三段：M4a 内核 → M4b 工具与召回 → M4c 提取回写；本文件为第二段，**前置：M4a 已验收**）· 用法：把下文整段粘贴给新开的实施会话。

---

请阅读 `D:\dev-projects\Agent\personal-agent\Agent.md`、`docs/design.md`（§8 已定稿）**和 `docs/research-m4-kb.md`，并通读参考源码 `D:\dev-projects\Agent\dsh-knowledge\`（重点：`src/tools.ts`、`src/recall.ts`、`src/retrieval.ts`）**。M4a 已实现 `internal/kb`（KB 接口 + SQLite FTS5 Provider + `kb/recall` 事件类型）。本段在 M4a 内核之上实现**工具消费面与召回注入**。

**M4b 范围**：

1. **工具 Consumer（注册进 tools，默认关闭，D10）**：
   - `kb_search(query, limit)` → 条目片段 + 来源 + score（只读）；
   - `kb_read(id)` → 完整条目（只读）；
   - `kb_add(title, body, type, tags)` → 显式写入（写，`source="manual"`）。
   - 三个工具随 `kb.enabled` 自动加入白名单（沿用 M3 run_command 单一开关模式），**默认关闭**。参数在 Execute 入口统一 JSON Schema 校验（D7）。

2. **召回注入（cmd/pa 编排，不改 loop，D4）**：
   - 会话开始时：把知识库轻量目录（名称+描述，不塞正文）注入系统提示词（`prompt` 分节，参考 `src/recall.ts` 的 catalog 注入）。
   - 每轮开始时：按用户输入调 `kb.Recall`（有界条数，config `recall_limit`，默认 3；0=关闭），注入上下文消息；**检索失败 fail-open**，不阻断回答（参考 `src/recall.ts`）。
   - 注入内容以 `kb/recall` 事件落日志（D3，事件类型 M4a 已声明，本段补实际写入路径）。

3. **`kb/add` 事件落日志（D3）**：显式写入时追加 `kb/add` 事件（条目摘要）。`kb_search`/`kb_read` 结果走 `tool/result`（模型实际看到，已满足 D3）。

4. **CLI**：`/kb-status`（条目数/库大小/最近写入）、`/kb-reindex`（重建 FTS 索引）。`kb.enabled` 决定 kb 服务/工具是否注册。

5. **config（internal/config 扩展，本段补充）**：
   ```yaml
   kb:
     enabled: false
     db_path: data/kb/knowledge.sqlite
     top_k: 5
     recall_limit: 3          # 每轮主动召回条数；0 = 关闭主动召回
     catalog: true            # 系统提示词注入轻量目录开关
   ```

**决策记录（必交）**：更新 `docs/decisions/2026-08-18-m4-kb-architecture.md`（在 M4a 基础上补充）：④ 工具消费面（三个工具的边界、白名单开关、D7 校验）；⑤ 召回注入机制（catalog + 有界 recall 如何在不改 loop 结构的前提下由 cmd/pa 编排，fail-open 依据）。提取回写决策留 M4c。

**约束**（严格遵守 design.md 第 10 节 D1–D10）：

- 不改 loop 的 turn/step 结构（D4）；kb 是能力接缝，Tool 三件套齐全（D2/D9）。
- **明确不做（本段）**：提取回写 Extract（M4c）、多知识库/挂载/标签权限矩阵、embedding/向量、文档解析、Web UI、远程 API。
- kb 工具默认关闭（D10）；`kb_search`/`kb_read` 只读、`kb_add` 写，沿用 M3 白名单门。
- 保持 CGO-free；**不新增任何第三方依赖**；Go 沙箱绕行沿用项目内缓存。
- 原有测试（含 M4a 新增）必须保持绿色；知识库数据落 `data/kb/`（不入库）。

**参考源码**：`D:\dev-projects\Agent\dsh-knowledge\`（重点文件见开头；**只借鉴思路，不照搬 TS 代码**）。M4a 已实现的内核（`internal/kb`）是本段直接基础。

**自测（全部通过后提交，提交信息含 M4b）**：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：三个工具注册/默认关闭（未启用时不注册）、`kb_add` 参数 JSON Schema 校验（坏参数拒绝）、`kb_add` 后 `kb_search` 能检索到且结果含来源、`kb_read` 返回完整条目、`kb/recall` 事件随注入落日志、`kb/add` 事件落日志、fail-open（召回/检索失败不阻断回答）、CLI `/kb-status` `/kb-reindex` 可用、原有 M4a 测试保持绿色。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash、ADR 更新路径。

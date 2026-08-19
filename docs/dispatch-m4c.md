# M4c 实施派发消息（控制会话 → 实施会话）——回答后提取回写

> 状态：已派发 2026-08-18（M4 拆三段：M4a 内核 → M4b 工具与召回 → M4c 提取回写；本文件为第三段，**前置：M4a、M4b 已验收**）· 用法：把下文整段粘贴给新开的实施会话。

---

请阅读 `D:\dev-projects\Agent\personal-agent\Agent.md`、`docs/design.md`（§8 已定稿）**和 `docs/research-m4-kb.md`，并通读参考源码 `D:\dev-projects\Agent\dsh-knowledge\`（重点：`src/extraction.ts`、`docs/architecture.md` 提取流程）**。M4a 已实现 `internal/kb` 内核（KB 接口 + SQLite FTS5 Provider），M4b 已实现工具与召回注入。本段补上 dsh-knowledge 的灵魂：**回答后提取回写**。

**M4c 范围**：

1. **提取回写（KB.Extract，M4a 预留的接口位）**：每轮回答结束后由 `cmd/pa`（组合根）调用 `kb.Extract(session, turn)`（**不改 loop 结构，D4**）：
   - `extraction_jobs` 幂等认领 `session:turn`（表在 M4a 建或不建均可，本段必须实现幂等）；重放不重复写。
   - 取本轮用户输入 + 最终回答，先 `Search` 既有条目作上下文，调**当前模型**（复用现有 `internal/llm`）输出**严格 JSON 候选**（type/title/body/tags，可含"跳过"）：运行时校验拒绝未知 type、越权字段、非 JSON（fail-closed）；只收明确陈述或已验证的长期知识（参考 `src/extraction.ts` + `docs/architecture.md` 提取流程；M4 裁剪掉挂载/候选审核/远程，仅直写）。
   - 失败 fail-open：**绝不影响下一轮回答**；失败/跳过以 `kb/extract` 事件记录（含 reason）。
   - 提取模型默认跟随当前会话模型（复用现有 llm 适配器），不引入新配置；ADR 说明取舍。

2. **`kb/extract` 事件落日志（D3）**：新增事件类型 `kb/extract`（提取结果：created/skipped/failed + reason）。`kb/recall`/`kb/add` 已在 M4a/M4b。

3. **config（internal/config 扩展，本段补充）**：
   ```yaml
   kb:
     enabled: false
     db_path: data/kb/knowledge.sqlite
     top_k: 5
     recall_limit: 3
     catalog: true
     extraction: true         # 回答后提取回写开关
   ```

**决策记录（必交）**：更新 `docs/decisions/2026-08-18-m4-kb-architecture.md`（在 M4a/M4b 基础上补充）：⑥ **提取回写机制**（幂等、严格 JSON fail-closed、不阻断回答、提取模型选择；对照 dsh-knowledge 的裁剪取舍：无挂载/候选审核/远程，仅直写）。

**约束**（严格遵守 design.md 第 10 节 D1–D10）：

- 不改 loop 的 turn/step 结构（D4）；kb 是能力接缝，Service/Provider/Tool 三件套齐全（D2/D9）。
- **明确不做**：embedding/向量检索、文档解析/分块摄入、PDF/OCR/网页抓取、Web UI、多知识库/挂载/标签权限矩阵、候选审核/远程 API、自动注入完整 RAG（M4 只做有界 recall + 模型驱动的 kb_search）。
- 提取与工具默认关闭（D10）；沿用 M3 白名单门。
- 保持 CGO-free；**不新增任何第三方依赖**；Go 沙箱绕行沿用项目内缓存。
- 原有测试（含 M4a/M4b 新增）必须保持绿色；知识库数据落 `data/kb/`（不入库）。

**参考源码**：`D:\dev-projects\Agent\dsh-knowledge\`（重点文件见开头；**只借鉴思路，不照搬 TS 代码**）。`internal/kb`（M4a）+ 工具与召回（M4b）是本段直接基础。

**自测（全部通过后提交，提交信息含 M4c）**：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：Extract 幂等（同一 session:turn 不重复写）、提取严格 JSON fail-closed（坏输出 → failed 事件不崩）、提取成功写入后能被 `kb_search` 检索、`kb/extract` 事件落日志（created/skipped/failed）、fail-open（提取失败不阻断回答）、config `extraction: false` 时跳过提取、原有 M4a/M4b 测试保持绿色。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash、ADR 更新路径。

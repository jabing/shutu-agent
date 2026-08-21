# M2 实施派发消息（控制会话 → 实施会话）

> 状态：已派发 2026-08-18 · 用法：把下文整段粘贴给新开的实施会话。

---

请阅读 `D:\dev-projects\Agent\shutu-agent\Agent.md` 和 `docs/design.md`，按设计基线实现 **M2 持久化与会话**（里程碑验收标准见 Agent.md 第 4 节）。

**M2 范围**（对应 design.md §2/§3/§6/§7 与 D8）：

1. `internal/store`：持久化抽象接口 + SQLite 实现（`modernc.org/sqlite`，纯 Go、CGO-free）。事件追加写入、启动重放；事件类型是文本、Data 是 JSON blob、带版本号字段（design.md §3）——**新事件类型不得要求迁移旧数据**。建议单库 `data/pa.db`（sessions 表 + events 表），实现可自定，但必须满足验收标准。
2. 多会话：REPL 增加 `/new`、`/list`、`/resume <id>` 命令；切换后历史完整恢复。
3. `internal/prompt` 分节组装：persona 分节内容从提示词文件加载（不再硬编码在 main.go）；分节机制支持独立增删分节而不改循环（design.md §7）；skills / knowledge 分节机制就位（M4 才注入内容）。
4. `internal/config`：config.yaml（model、base_url 可选、data_dir），缺失时用默认值；API Key 仍只走环境变量（D10 配套纪律）。
5. `internal/llm/deepseek` 重试策略：5xx / 429 / 网络错误退避重试 2–3 次，尊重 ctx 取消；4xx 认证错误不重试。策略放在适配器内部（design.md §6：provider 自有权责）。

**约束**（严格遵守 design.md 第 10 节 D1–D10）：

- 不改 loop 的 turn/step 结构（D4）；不实现 kb、bash、Web、子代理、压缩等任何 M2 之外的功能（越界即退回）。
- 循环仍严格串行（D5）；模型可见输入仍必须落日志（D3）；历史仍是日志的派生值（D1）。
- 保持 CGO-free。若 Go 命令被沙箱拦截，沿用项目内缓存绕行：GOMODCACHE / GOCACHE / GOPATH 指向项目内 `.gomodcache` / `.gocache` / `.gopath`（已入 .gitignore）。
- 原有测试必须保持绿色。

**参考源码**：`D:\dev-projects\Agent\deepseek-harness\packages\session\session-persistence*`（持久化/重放）、`packages\core\system-prompt`（分节组装）。只借鉴思路，不照搬代码。

**自测（全部通过后提交，提交信息含 M2）**：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：store 重放（持久化后重载，事件逐条一致、派生历史一致）、多会话恢复、提示词分节、重试（httptest 模拟 429→200）。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash。

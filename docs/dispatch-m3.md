# M3 实施派发消息（控制会话 → 实施会话）

> 状态：已派发 2026-08-18 · 用法：把下文整段粘贴给新开的实施会话。

---

请阅读 `D:\dev-projects\Agent\shutu-agent\Agent.md` 和 `docs/design.md`，按设计基线实现 **M3 安全与完善**（里程碑验收标准见 Agent.md 第 4 节）。

**M3 范围**（对应 design.md §5、D10 与 Agent.md 路线图 M3 行）：

1. **工具白名单**：config.yaml 增加 `tools.enabled`（按名称启用/禁用，design.md §5）。未启用 ⇒ 拒绝执行（Execute 门）。**默认白名单只含只读工具** `get_time`、`read_file`。
2. **执行类工具 `run_command`**：随 M3 安全白名单一起上（design.md §5，D10 落地）。要求：
   - 仅当 `tools.run_command.enabled: true` 时注册/可用，默认关闭；
   - 单命令执行（Windows 上经 cmd 或 PowerShell 的单行命令），固定工作目录，不暴露交互式 shell；
   - **环境清除**：执行前从环境变量中移除含 KEY / SECRET / TOKEN / PASSWORD / API 的条目（参考 dsh bash 的环境清除思路）；
   - 超时与输出上限走统一管道（见下）。
3. **超时（Execute 管道固定环节）**：每次工具 Execute 用 `context.WithTimeout` 包裹，超时可配置（全局 `tools.timeout` 默认 30s，`run_command` 可单独覆盖）；超时作为 `tool/error` 事件落日志（D3）。
4. **输出截断 / spill**：工具输出超过 `tools.output_limit`（默认 64KB）时截断，全文落盘到 `data/spill/<session>-<seq>.txt`；`tool/result` 事件记录截断文本 + 定位符（模型可见 ⇒ 落日志 D3）。
5. **取消完善**：Ctrl+C 即时生效——流式中断（已有，补测试）、工具执行中断（`exec.CommandContext`）；确认取消后内存日志与持久化状态一致。
6. **CLI 完善**：`--config <path>` 启动参数；`/help` 输出完整命令表。（Web UI 属"可选"，**明确不做**，推后另行决策。）

**决策记录（必交）**：写 `docs/decisions/2026-08-18-m3-sandbox-scope.md`，记录沙箱范围评估结论：为什么采用"白名单 + 超时 + 截断 + 环境清除"而非 OS 级沙箱/容器；放弃的方案；残余风险与后果（模板见 Agent.md 第 6 节）。

**约束**（严格遵守 design.md 第 10 节 D1–D10）：

- 白名单、超时、截断都是 tools 包的策略层或执行管道，**不进 loop**；loop 的 turn/step 结构不改（D4）。
- 不实现完整 shell/终端、kb、Web、子代理、压缩（越界即退回）。`run_command` 是唯一的执行类工具，且默认关闭。
- 模型可见输入仍必须落日志（D3）；历史仍是日志的派生值（D1）；循环仍严格串行（D5）。
- 保持 CGO-free；Go 沙箱绕行沿用项目内缓存（`.gomodcache` / `.gocache` / `.gopath`，已入 .gitignore）。
- 原有测试必须保持绿色。

**参考源码**：`D:\dev-projects\Agent\deepseek-harness\packages\shell\`（执行工具的超时与环境清除思路）、`packages\spill\`（spill 三件套：定义/本地后端/策略）、`packages\guard\`（工具超时插件）。只借鉴思路，不照搬代码。

**自测（全部通过后提交，提交信息含 M3）**：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：白名单拒绝（未启用工具）、超时（sleep 工具被掐断并落 tool/error）、截断 + spill 文件生成与定位符、取消中断执行中命令、`run_command` 默认不注册/显式启用后可执行、环境清除。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash、ADR 路径。

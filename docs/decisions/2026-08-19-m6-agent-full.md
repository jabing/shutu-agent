# ADR 2026-08-19：M6 能力补全——与 dsh Agent 能力面对齐

> 状态：已定 · 背景 → 决策 → 理由 → 后果（含放弃的方案）

## 背景

M5 四段（jobs/subagent/compaction/skill）完成后，github.com/jabing/shutu-agent 已具备 dsh 八个能力族的对齐实现。用户问"补齐真正的功能差距后是否与 dsh Agent 有相同的代码能力"，结论：任务类功能补齐后个人任务能力相当，但**代码能力还有四个实质缺口**（代码沙箱、MCP 工具生态、LSP、fs/workspace 封装），且架构上双端/插件系统永不追平（D4 取舍，是优点非缺陷）。

本 ADR 把"能力面补全"定为一个新里程碑族 **M6**，全部以接缝方式挂现有薄核心（D4），逐段验收（M5 同款纪律）。

## 决策

M6 拆六段，每段一个能力接缝（Service/Provider/Tool 三件套 + 事件 + config + D10 默认关）：

| 段 | 能力 | 接缝模块 | 对照 dsh 参考 | 说明 |
|---|---|---|---|---|
| **M6a** | 定时调度 | `internal/schedule` | `packages/schedule/` | Registry + Provider；间隔/cron 触发；**触发不直接进主循环**——到期生成一条 `schedule/fire` 事件 + 可选入队 job（D5） |
| **M6b** | 任务规划 | `internal/plan` | `packages/goal/` `plan/` `todo/` | goal → plan → todo 三层 + 规划/推进工具 + 事件；执行可委托子代理（复用 M5b） |
| **M6c** | 长期记忆 | `internal/spill` | `packages/spill/` | 跨会话记忆 Provider；自动沉淀/按需召回；**与 kb 的关系**：kb=可检索知识库（显式），spill=对话衍生记忆（自动），独立接缝不合并（见后果） |
| **M6d** | 人工审批 | `internal/interact` | `packages/interaction/` | 审批请求/响应接缝；敏感工具执行前经 interact 门（CLI 侧交互，非 Web）；D3 事件 |
| **M6e** | 代码沙箱 | `internal/code` | `packages/code-runtime/` `e2b/` | 沙箱 Provider 接口 + 本地子进程隔离实现（超时/配额/无网络默认）；`code_run` 工具补强 M3 的 `run_command` |
| **M6f** | 工具生态 | `internal/mcp` + `internal/fs` | `packages/mcp/` `fs/` `workspace/` | MCP 客户端接缝（外部工具）+ fs/workspace 统一封装 |

**顺序**：M6a → M6b → M6c → M6d → M6e → M6f（依赖：M6b 执行依赖 M5b 子代理；M6f 的 mcp 最重最后）。

## 理由

1. 全部走接缝 + PreStep/工具扩展点，**零循环改动**（D4）——与 M5 同款，风险最低。
2. 逐段验收（M5 四段成功经验），每段独立可回退。
3. 补的是"能力面"非"架构面"：双端平台/插件系统/SDK/workflow 明确不做（D4 取舍）。

## 后果

- **放弃**：插件系统、host/client 双端、SDK、workflow 编排、browser 自动化（这些是 dsh 平台属性，个人 Agent 不需要，且引入会破坏 D4 薄核心）。
- **暂缓**：LSP/编辑器集成（需编辑器侧配合，价值取决于是否在 IDE 用 Agent）、remote API/SDK（属 Web 接口类，用户已排除）。
- **零新第三方依赖约束面临首个例外**：M6f 的 MCP 客户端需要 MCP Go SDK 或自实现（MCP 协议是 JSON-RPC over stdio/HTTP，**自实现可行且更符合零依赖纪律**；派发 M6f 时按"优先自实现，SDK 仅当协议复杂度超限才评估"决策）。其余各段保持零新依赖。
- **spill 与 kb 边界**：kb 保留为显式知识库（用户可见、可检索、FTS5），spill 为自动对话记忆（无需用户显式写）；二者检索可复用同一 store 后端但接缝独立，避免把"自动沉淀"和"显式知识"搅在一个接口里（D9 保持）。
- **M6e 沙箱安全**：本地子进程沙箱是"受控隔离"而非强隔离（进程边界 + 超时/配额/默认无网络），强隔离（e2b 云端）明确不做；安全等级记录在 D10 演进。
- 本 ADR 与 design.md §11、Agent.md §4 同步更新（双向同步）。

## 验收总则（逐段）

每段按 M5 纪律：先 ADR 细化 + dispatch 文档 → 派发实施 → 控制会话亲自 vet/test/build → 对照 D1–D10 审 diff → 通过才标记。默认关（D10），零新依赖（M6f 例外见上）。

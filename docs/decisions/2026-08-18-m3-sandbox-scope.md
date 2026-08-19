# ADR: M3 沙箱范围——"白名单 + 超时 + 截断 + 环境清除"，不引入 OS 级沙箱

- 状态：**已定**（2026-08-18）
- 关联：design.md §5、D10；Agent.md 路线图 M3

## 背景

M3 需要给执行类工具 `run_command`（design.md §5 / D10 落地，默认关闭）提供安全边界，同时保证里程碑验收标准：未白名单工具拒绝执行、取消即时生效、长输出不爆上下文。实现前评估"需要做到什么强度的隔离"。

威胁模型（个人本地工具，design.md §0 明确不做多用户/云端）：不是恶意对手攻防，而是——
1. 模型误调用（调了不该调的工具 / 命令写错）；
2. 命令挂死（无界等待）；
3. 输出爆上下文（长输出塞满模型请求）；
4. 凭据泄漏（子进程隐式继承 API Key / 密码）。

## 决策

采用 **"白名单 + 超时 + 截断 + 环境清除"** 四件套作为 M3 的沙箱边界，全部落在 `tools` 包（策略层 + Execute 管道），不引入 OS 级沙箱/容器：

- **白名单** `tools.enabled`：按名称启用/禁用，默认只含只读工具 `get_time`、`read_file`；未启用 ⇒ Execute 门拒绝。
- **超时** `tools.timeout`（默认 30s，`run_command` 可覆盖）：每次 Execute 用 `context.WithTimeout` 包裹；超时落 `tool/error`。
- **截断 / spill** `tools.output_limit`（默认 64KB）：超限截断，全文落 `data/spill/<session>-<seq>.txt`，`tool/result` 记录截断文本 + 定位符。
- **环境清除**：`run_command` 执行前移除名字含 `KEY`/`SECRET`/`TOKEN`/`PASSWORD`/`API` 的环境条目（参考 dsh `scrubbedParentEnv`，追加 `API`）。

取消（Ctrl+C）经 `signal.NotifyContext` + 上下文贯穿流式与工具执行；`run_command` 的进程终止：Windows 杀直系进程（输出走临时文件，孙进程不占管道），Unix 用进程组 `kill(-pgid)`。

## 理由

1. **威胁模型匹配**：四件套逐一覆盖上面四条主要风险；个人单机场景没有多租户、不可信输入的攻击面。
2. **硬约束排斥 OS 沙箱**：本项目硬约束 **CGO-free、Windows 可无工具链构建**（design.md §9）。bwrap / Landlock / Seatbelt 是 Linux 内核机制，Windows 上不可用；Windows 原生隔离（Job Object、受限令牌、AppContainer）要么是大量系统调用重活，要么对 `cmd /C` 这类普通命令限制过强。
3. **容器违背单二进制目标**：Docker 等增加部署依赖与启动成本，个人工具不值当。
4. **策略层进 `tools` 包符合 D4**：白名单/超时/截断是 Execute 管道的固定环节，循环的 turn/step 结构零改动。
5. **参考 dsh 同构**：dsh 也是"白名单（tools/pre-execute）+ 超时（timeout-policy）+ 截断（spill-policy）+ 环境清除（scrubbedParentEnv）"为默认防线，OS 沙箱是可选 executor（bash-sandbox）。M3 只落地默认防线。

## 后果

### 放弃的方案

- OS 级沙箱：bwrap / Landlock / Seatbelt（Windows 不可用）、Windows Job Object / 受限令牌 / AppContainer（实现成本高、约束过强）。
- 容器 / Docker（部署负担、违背单二进制）。
- 网络防火墙 / 网络白名单（`run_command` 默认可访问网络）。
- 只读根文件系统 / 强制文件访问控制（对个人工作流过重）。

### 残余风险与后果

- `run_command` 拥有**完整用户权限**：可读写文件、访问网络、启动任意命令。白名单只是门禁（能调用才放行），不是强隔离；`cmd /C` 单行仍支持 `&&`、`|` 等组合。
- **环境清除是启发式**（按名字子串匹配），不排除所有凭据；且它只拦"隐式继承"，模型若显式把敏感值写进命令行参数，该值会出现在 `tool/result` 日志里（D3 要求模型可见即落日志，属预期）。
- **Windows 进程终止只保证直系进程**：`exec.CommandContext` 只杀 `cmd.exe`；孙进程（如 ping/sleep）可能残留。输出改走临时文件使 `Wait` 不被孙进程占住的管道阻塞，因此中断对 agent 是即时的；残留孙进程是文档化的残余风险。`taskkill /T` 在受限/沙箱环境下可能 Access Denied，故不依赖它做树终止。
- **spill 是 best-effort**：写盘失败时保留内联输出，成功调用不会被降级为错误（长输出届时可能撑上下文，属异常路径）。
- 持久化/日志不受影响：超时（`tool/error`）与截断+定位符（`tool/result`）都落日志，历史仍是日志派生值（D1/D3 保持）。

### 何时可重评

M5 出现子代理、多租户、或把 agent 暴露给他人使用等明确需求时，再评估 OS 级隔离（Windows Job Object / 容器 / 受限账户），届时先出独立决策记录。

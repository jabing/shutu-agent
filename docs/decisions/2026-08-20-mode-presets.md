# ADR 2026-08-20：模式预设接缝（agent.mode：极简 / 标准 / PTC）

- 状态：已接受
- 日期：2026-08-20
- 决策驱动者：用户需求「需要支持除创造模式外的另外三种模式（标准 / PTC / 极简），并可通过设置修改」

## 背景

dsh 提供四种 agent preset（`apps/cli/config/agent-presets/`）：标准（standard，完整编码 agent）、PTC（code，标准 + Code Mode SDK 程序化组合）、极简（minimal，固定 persona + 持久 shell + str_replace_editor 双工具）、创造（cordis，标准 + 插件自修改）。github.com/jabing/shutu-agent 无插件系统（编译期接缝，见 `3271a7c` 与用户明确拍板「不考虑运行时插件能力」），因此：
- 创造模式架构上不适用（其核心 cordis_mount 评估模型写的 JS / 运行时检查 / preset 创作建立在插件系统上）——用户明确排除。
- 三种可支持的模式实现为 **config 驱动的编译期接缝**：一次运行一个模式，通过 `agent.mode` 设置切换（重启生效）。

## 决策

### D-MODE-1 模式语义
顶层 `mode:` 配置项，三选一（非法值启动时 fail-closed，照 LLM provider 先例）：
- `minimal`（极简）：固定 persona + 仅持久 shell（`terminal_*`）+ 文件编辑（`fs_*`）+ M1 基础只读（`get_time`/`read_file`）。对齐 dsh minimal 的「持久 shell + 文件编辑」双工具语义（str_replace_editor 由 fs_* 三件套等价承担）。
- `standard`（标准）：全部已实现能力（现状默认），各能力仍由各自 `enabled` 独立开关控制（D10 保持）。
- `code`（PTC）：standard 全部能力 + 系统提示词注入「程序化操作（Code Mode）」段，提示模型优先用 `code_run` 沙箱把多步操作写成一段程序一次执行。诚实近似：无 TS 运行时，不投影 TS SDK，`code_run` 承担程序化执行（用户 2026-08-20 拍板选择该形态）。
- 默认 `standard`（`Mode == ""` → standard）；`minimal`/`code` 是真正改变行为的预设。

### D-MODE-2 mode 是能力开关推导，不新增注册层
`mode` 在 `config.applyDefaults` 末尾解耦为既有能力开关的赋值 + 白名单推导，**不改任何 register\* 的 D10 门**（它们读 `cfg.X.Enabled`，自然生效）：
- `minimal`：`Terminal.Enabled=true`、`Fs.Enabled=true`；其余全部能力 cap（KB/Jobs/Subagent/Compaction/Skill/Schedule/Plan/Spill/Interact/Code/Mcp/Web/Eval/LLM-multimodal）`Enabled=false`；`Tools.Enabled` 覆盖为仅 `get_time`+`read_file`+`terminal_*`+`fs_*`（执行白名单）。
- `standard`：不触碰（现状）。
- `code`：不触碰（现状）+ D-MODE-3 提示词注入。
这样极简模式的模型可见工具面 = 注册面 = 白名单面，三者一致（dsh minimal 同样只暴露两工具）。

### D-MODE-3 提示词按模式组装（cmd/pa 组合根）
`prompt.Builder` 组装随模式分支：
- `standard`：`prompt.LoadDir(cfg.PromptsDir)`（现状）。
- `minimal`：`prompt.New(固定 persona 常量)` —— 单 section 固定 persona（对齐 dsh minimal `complete:true`：全局身份/工具引导等不再追加文本）；工具目录仍渲染（模型需知可用工具）。
- `code`：`prompt.LoadDir(cfg.PromptsDir)` 后 `Add(Section{Name:"code-mode", Text: 固定 Code Mode 段})`。
固定 persona / Code Mode 段是代码内常量（非 prompts_dir 文件），保证三模式可复现、可测试。

### D-MODE-4 PTC 的诚实边界
`code` 模式不引入 TS 运行时、不投影工具 SDK；它以「提示词偏好 + `code_run` 程序化组合」近似 dsh Code Mode 的单程序多步语义。差异在 ADR 与 Agent.md 明确记录：dsh 是呈现层 SDK（工具注册表投影 TS API），本项目是行为层偏好（提示模型把可批量操作合并进 code_run）。

### D-MODE-5 设置入口与生效
- 唯一设置入口：`config.yaml` 的 `mode:` 字段（无 Web UI、无运行时切换——编译期接缝，重启生效）。
- `pa --help` / printHelp 状态块与 `/mode-status`（若有）显示当前模式。实施范围以最小为准：状态显示随现有 printHelp 状态块，不新增命令（避免无谓膨胀）。

### D-MODE-6 纪律
- 零新依赖、CGO-free；`loop.go` 不动（D4）；prompt 组装只发生在组合根。
- 默认 `standard` ⇒ 现有默认行为零变化（D10 语义不变：未设 mode 时各能力仍默认关）。
- `minimal` 是**预设优先**：用户显式开启的 kb/terminal 之外能力在 minimal 下被忽略（文档说明；预设语义优先于单能力开关）。

## 后果
- 用户可经 `config.yaml` 在极简/标准/PTC 三模式间切换，满足「可通过设置修改」。
- 创造模式明确不支持（架构排除），本 ADR 记录边界。
- 工具面按模式收敛：minimal 极简可审计；code 仅提示词差异 + code_run。

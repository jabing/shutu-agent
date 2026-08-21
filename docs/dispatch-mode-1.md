# Mode-1 派发：config agent.mode 段（三模式 + applyDefaults 推导 + 校验 + config.yaml + 测试）

> 模式预设接缝 ADR `docs/decisions/2026-08-20-mode-presets.md`（D-MODE-1/2/6）。本文件是 **Mode-1** 契约：config 层。前置：全部既有能力已交付。cmd/pa 接线在 Mode-2。

## 纪律

- 零新依赖、CGO-free；只动 internal/config/config.go + internal/config/config_test.go + config.yaml；gofmt；不改 loop。
- 默认 `standard` ⇒ 现有默认行为零变化（未设 mode 时各能力仍默认关，D10）。
- 提交 1 个：`Mode-1: config agent.mode 段（minimal/standard/code + applyDefaults 推导 + 校验）`

## 已知现状（实施时请通读对应区域，勿猜）

- `internal/config/config.go`：`Config` struct 顶层字段（line 159-181，最后是 `Eval EvalConfig` line 180）；`Load(path)`（line 675-696，unmarshal 后 applyDefaults）；`applyDefaults(cfg)`（line 700 起，各 cap enabled 时 append `cfg.Tools.Enabled`，eval 白名单在 line 1047-1048 附近，是当前末尾）；`defaultEnabledTools = []string{"get_time","read_file"}`（line 154）。
- 各 cap 的 Enabled 字段：Terminal（`Terminal.Enabled`）、Fs、KB、Jobs、Subagent、Compaction、Skill、Schedule、Plan、Spill、Interact、Code、Mcp、Web、Eval（`Eval.Enabled`）、`Tools.RunCommand.Enabled`、`LLM.Multimodal.Enabled`——全部是 `bool yaml:"enabled"`（实施时逐一核对字段名）。
- 现有 config_test.go 的测试模式（table 或独立函数）。

## 变更清单（精确）

### 1. internal/config/config.go
- **常量**（const 块末尾、DefaultEval* 后）：
```go
	// Mode presets (ADR 2026-08-20-mode-presets.md D-MODE-1): the top-level
	// mode selects the agent's capability preset — minimal (极简: 固定 persona
	// + 持久 shell + 文件编辑), standard (标准: 全部已实现能力, 默认), code
	// (PTC: 标准 + 程序化操作 Code Mode 提示词段). An unknown value fails
	// closed at Load (like the LLM provider). 默认 standard ⇒ 现有默认行为零
	// 变化 (D10).
	DefaultMode = "standard"
	ModeMinimal = "minimal"
	ModeStandard = "standard"
	ModeCode    = "code"
```
- **Config struct**（`Eval EvalConfig` 后加）：
```go
	// Mode selects the agent capability preset (D-MODE-1): minimal | standard
	// | code; default standard. minimal is preset-first (D-MODE-6): 能力开关
	// 与白名单被覆盖, 用户显式开启的其余能力在 minimal 下被忽略.
	Mode string `yaml:"mode"`
```
- **Load**（unmarshal + applyDefaults 之间或之后加校验）：
```go
	applyDefaults(&cfg)
	// Mode fails closed on unknown values, like the LLM provider route
	// (D-MODE-1): never silently fall back.
	switch cfg.Mode {
	case ModeMinimal, ModeStandard, ModeCode:
	default:
		return Config{}, fmt.Errorf("config: invalid mode %q (want minimal|standard|code)", cfg.Mode)
	}
```
（注意顺序：校验在 applyDefaults 之后，因 applyDefaults 把空 Mode 置为 DefaultMode。）
- **applyDefaults**：
  - 开头（`if cfg.Model == ""` 前或后）加：
```go
	if cfg.Mode == "" {
		cfg.Mode = DefaultMode
	}
```
  - **末尾**（eval 白名单 append 之后、函数体结束前）加 minimal 分支：
```go
	// D-MODE-2 (ADR 2026-08-20-mode-presets.md): minimal 模式是预设优先 ——
	// 只保留持久 shell + 文件编辑 + M1 基础只读；其余能力 cap 全关、白名单
	// 整体重置为 minimal 集合。register* 的 D10 门读这些 Enabled, 因此注册面
	// 与白名单面自动收敛。standard/code 不触碰 (现状). 必须放在所有既有
	// append 之后, 否则后续 append 会把用户开启的其余工具加回白名单.
	if cfg.Mode == ModeMinimal {
		cfg.Terminal.Enabled = true
		cfg.Fs.Enabled = true
		cfg.KB.Enabled = false
		cfg.Jobs.Enabled = false
		cfg.Subagent.Enabled = false
		cfg.Compaction.Enabled = false
		cfg.Skill.Enabled = false
		cfg.Schedule.Enabled = false
		cfg.Plan.Enabled = false
		cfg.Spill.Enabled = false
		cfg.Interact.Enabled = false
		cfg.Code.Enabled = false
		cfg.Mcp.Enabled = false
		cfg.Web.Enabled = false
		cfg.Eval.Enabled = false
		cfg.LLM.Multimodal.Enabled = false
		cfg.Tools.RunCommand.Enabled = false
		cfg.Tools.Enabled = append([]string(nil), minimalEnabledTools...)
	}
```
- **minimalEnabledTools**（`defaultEnabledTools` 附近）：
```go
// minimalEnabledTools is the minimal preset's exact execution whitelist (ADR
// 2026-08-20-mode-presets.md D-MODE-2): M1 基础只读 + 持久 shell (terminal_*)
// + 文件编辑 (fs_*). 工具名须与各包常量一致 (terminal.go/fs.go).
var minimalEnabledTools = []string{
	"get_time", "read_file",
	"terminal_start", "terminal_write", "terminal_read", "terminal_signal", "terminal_stop",
	"fs_read", "fs_write", "fs_list",
}
```
（实施时核对 terminal_*/fs_* 工具名是否与 internal/terminal、internal/fs 的常量完全一致；若有出入以实际常量为准并在报告注明。）

### 2. config.yaml
- 顶层加（`model:` 附近即可）：
```yaml
# Agent 模式预设 (ADR 2026-08-20-mode-presets.md): standard (标准, 默认: 全部
# 能力按各自 enabled 开关) | minimal (极简: 固定 persona + 持久 shell + 文件
# 编辑, 其余能力忽略) | code (PTC: 标准全部 + 程序化操作 Code Mode 段, 用
# code_run 组合多步). 重启生效; 未知值启动报错.
mode: standard
```

### 3. internal/config/config_test.go
用例（模式照现有测试风格）：
1. `TestModeDefault`：零值 Config（Load 缺省文件路径或 Config{} + applyDefaults）→ Mode == "standard"。
2. `TestModeMinimalWhitelist`：mode: minimal → Terminal.Enabled==true、Fs.Enabled==true、KB/Jobs/Subagent/Web/Eval/Code/Interact/Compaction/Skill/Schedule/Plan/Spill/Mcp/LLM.Multimodal/Tools.RunCommand 全部 false；`Tools.Enabled` 精确等于 minimalEnabledTools（用 reflect.DeepEqual 或逐元素）。
3. `TestModeMinimalPresetFirst`：mode: minimal + kb.enabled: true + web.enabled: true → 仍全 false（预设优先，D-MODE-6）。
4. `TestModeStandardUnchanged`：mode: standard + kb.enabled: true → KB.Enabled 仍 true；其余 cap 不受影响（与不设 mode 等价）。
5. `TestModeCodeKeepsCaps`：mode: code + kb.enabled: true + terminal.enabled: true → 与 standard 一致（KB true、Terminal true；无 cap 被 code 改动）。
6. `TestModeInvalidFailsClosed`：mode: turbo → Load 返回 error（含 "invalid mode"）。
7. `TestModeMinimalTerminalToolsRegistered`（可选，仅 config 层）：minimal 时白名单含全部 5 个 terminal_* 与 3 个 fs_*。

## 验证

`go build ./...` + `go test -count=1 ./internal/config/` 全 PASS 后提交；随后 `go test -count=1 ./...` 全绿确认（默认 standard 不应破坏任何既有测试）。

## 环境

- Go：`C:\Program Files\Go\bin\go.exe`；env：`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'`；提交身份 `-c user.name='Personal Agent' -c user.email='dev@shutu-agent.local'`；pwsh，workdir=`D:\dev-projects\Agent\shutu-agent`。

## 报告（简短）
提交 hash + go test 结果 + 偏离说明（特别是 terminal/fs 工具名若有出入）。不要贴代码。

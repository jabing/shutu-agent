# M9-1 派发：internal/terminal 持久 shell 会话（Session + 有界缓冲 + Windows 实现 + 就绪判定）

> 里程碑 M9 持久终端（ADR `docs/decisions/2026-08-20-m9-terminal.md`）。本文件是 **M9-1（前半）** 契约：`internal/terminal` 包的核心 —— `Session`（持久 shell 子进程 + 管道）、有界 scrollback（dsh BoundedTextBuffer 移植）、Windows 实现、写后就绪判定、单测。**M9-2（config + D3 事件 + 模型工具五件套 + /term 命令 + wiring）** 在后续派发。

## 0. 纪律

- **不改 `internal/loop/loop.go` 的 turn/step 结构**（D4）；主循环串行（D5）；**零新第三方依赖**；CGO-free；原有测试全绿。
- **Windows-first**：本段实现 `exec_windows.go`（cmd.exe /Q 持久进程）；非 Windows 留 stub 或 `exec_unix.go` 最小实现（`/bin/sh -i`），但验收以 Windows 为准。
- 每模块阶段提交（commit message 前缀 `M9-1`）。

## 1. 范围

**做**：
1. `internal/terminal/buffer.go`：`BoundedTextBuffer`（dsh BoundedTextBuffer 移植）。
2. `internal/terminal/session.go`：`Session` 接口/结构（Write/Read/Consume/Signal/Close/Status/ID）+ 就绪判定（静默推断 idle / timeout / session_exit）。
3. `internal/terminal/session_windows.go`：Windows 实现（cmd.exe /Q + 管道 + 子进程树终止）。
4. 单测（真实子进程，短 idle 保稳）。

**不做（本段）**：config terminal 段、D3 事件、模型工具、/term 命令、Registry（M9-2）。

## 2. 类型契约（internal/terminal）

```go
// buffer.go
// BoundedTextBuffer 有界文本缓冲（dsh BoundedTextBuffer 移植，M9 ADR D-M9-3）：
// 字节上限 + 可选行数上限，UTF-8 安全截断（保尾丢最早），dropped 标记。
type BoundedTextBuffer struct{ ... }
func NewBoundedTextBuffer(maxBytes int, maxLines int) *BoundedTextBuffer // maxLines<=0 不限行
func (b *BoundedTextBuffer) Append(text string)
func (b *BoundedTextBuffer) Snapshot() (text string, truncated bool)
func (b *BoundedTextBuffer) Consume() (text string, truncated bool) // 自上次 Consume 后的 delta
func (b *BoundedTextBuffer) Empty() bool

// session.go
// WaitReason 是 Write 返回就绪的原因（M9 ADR D-M9-4）。
type WaitReason string
const (
    WaitStdinRead   WaitReason = "stdin_read"    // 静默推断：自最后输出起 idle
    WaitTimeout     WaitReason = "timeout"       // 绝对超时
    WaitSessionExit WaitReason = "session_exit"  // 进程退出
)

// WriteResult 是一次 Write 的返回。
type WriteResult struct {
    Viewport  string     // 本次写入后自上次 Consume 以来的新输出
    Wait      WaitReason
    Truncated bool       // 缓冲被截断（丢弃过最早输出）
    Status    SessionStatus
}

type SessionStatus struct {
    Kind     string // "running" | "exited"
    ExitCode int    // exited 时有效
}

// Session 是持久 shell 子进程句柄。
type Session struct{ ... }

// NewSession 启动持久 shell。opts.Shell 空 → "cmd.exe"；opts.Args 追加。
// opts.Workdir 空 → 继承；opts.Env 空 → scrubbedEnv（照 internal/jobs scrubbedEnv，
// 但本包自实现同款，不 import jobs）。opts.IdleMS（默认 500）/TimeoutMS（默认 30000）/
// ScrollbackMaxBytes（默认 64KiB）/ScrollbackLines（默认 2000）。
func NewSession(opts SessionOpts) (*Session, error)

func (s *Session) ID() string
func (s *Session) StartedAt() time.Time
func (s *Session) Status() SessionStatus

// Write 写 stdin 并等待就绪（D-M9-4）：追加 submit 为 true 时行结束（\r\n）。
// 返回期间新输出与就绪原因。进程已退出 → 错误。
func (s *Session) Write(text string, submit bool) (WriteResult, error)

// Read 快照 scrollback 的第 offset 行起至多 count 行（照 dsh read 语义）；越界返回空。
func (s *Session) Read(offset, count int) (text string, truncated bool)

// Consume 消费自上次以来的 delta。
func (s *Session) Consume() (text string, truncated bool)

// Signal 尽力而为的信号（Windows 见 D-M9-1 局限）：
//   - "stop"：终止会话（同 Close 的进程树终止 + 状态置 exited）
//   - "interrupt"：Windows 向 stdin 写 "\x03"（尽力而为，非信号语义）
//   - 其他 → 错误
func (s *Session) Signal(kind string) error

// Close 终止子进程树 + 等待退出 + 释放缓冲；幂等。
func (s *Session) Close() error
```

## 3. Windows 实现契约（session_windows.go）

- **spawn**：`exec.Command("cmd.exe", "/Q", args...)`（`/Q` 关 echo 提示符回显）；`cmd.Stdin` = 写入端管道、`Stdout/Stderr` = 同一读取端管道（合并流，照 jobs 心智）。
- **stdout 泵**：一个 goroutine `io.Copy` stdout/stderr → `BoundedTextBuffer.Append`；进程退出 → `Close()` 后标记 exited。所有输出只进缓冲，不进日志。
- **stdin 写**：直接写 `cmd.Stdin`（管道）。`submit=true` 追加 `\r\n`（Windows 行结束）。
- **就绪判定**（Write 内）：写后轮询（`time.Ticker` ~50ms）——
  - 自缓冲最后追加时间起持续静默 `idle_ms` → `WaitStdinRead`
  - 绝对 `timeout_ms` → `WaitTimeout`
  - 进程已退出（Wait 完成）→ `WaitSessionExit`
  - 判定期间收集 delta 返回。
- **进程树终止**：`cmd.Process.Kill()`（照 `internal/jobs/exec_windows.go` `killJobTree` 同款；Windows 无进程组，taskkill 不可用，杀直接子进程为文档化残余风险）。
- **状态**：`running` → 进程 Wait 返回后 `exited`（记录 exit code）。进程中途退出时 Write/Read 返回错误或 `WaitSessionExit`。

## 4. 就绪判定的测试稳定性

- 单测用**真实子进程**（`cmd.exe /Q`），但所有等待用**短 idle**（如 100ms）与**短 timeout**（如 2s）以保测试快且稳；就绪分支用确定性场景触发：
  - `WaitStdinRead`：`echo hello`（短输出 + 静默）→ 快返，Viewport 含 hello。
  - `WaitTimeout`：`ping -n 10 127.0.0.1` 类长命令（输出持续/低频）→ 到 timeout 仍返回。
  - `WaitSessionExit`：`exit` → 进程退出，Write/Status 报 exited。
- 全部测试跑在 Windows（本机）。

## 5. 测试要求（internal/terminal/terminal_test.go）

1. BoundedTextBuffer：append 超字节截断（保尾）+ truncated；超行截断；UTF-8 安全（不劈多字节字符）；Consume delta 语义；Snapshot 全量。
2. NewSession：默认 shell（cmd.exe）、startedAt、ID 非空；workdir 生效（`cd` 后 `cd` 验证？——用持久会话 `cd <tmp>` 然后 `cd` 输出路径验证状态保持）。
3. Write/Read/Consume 往返：`echo hello` → Write(submit) 返回含 hello；Read(0, count) 快照含 echo 行与 hello；Consume 后 delta 清空。
4. **状态保持**：Write `cd <tmpdir>`（submit）→ 再 Write `cd`（submit）→ 输出含 tmpdir（跨命令 cwd 保持）。
5. 就绪三分支（§4）。
6. Signal("stop") → 进程终止 + Status exited + 幂等 Close；Signal("interrupt") 不报错（尽力而为）。
7. 进程退出自动 exited：Write `exit` → Status().Kind == "exited"。
8. 凭证 scrubbed：Session env 不含含 "KEY"/"TOKEN" 的环境变量（可对子进程 `set` 输出断言，或直接检查 opts.Env 构造）。

## 6. 提交与报告

- 每模块阶段提交（`M9-1: ...`）：buffer → session → windows 实现 → 测试。
- 完成后 `go vet ./...` / `go test -count=1 ./...` / `go build ./...` 全绿再报告（只跑本包 + 全量）。
- 报告：改动文件清单、实现决策（对照本契约的偏离，尤其就绪判定与 Windows 局限）、跑过的命令、测试结果。

# M6f-3 实施派发消息（控制会话 → 实施会话）——fs 接缝 + 安全文件操作 + 工具/事件/config/接线

> 状态：待 M6f-2 验收后派发 2026-08-19（M6 能力补全六段，ADR `2026-08-19-m6-agent-full.md`；本文件为 M6f 第三派发：fs 安全文件操作）· 用法：M6f-2 验收通过后，把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\shutu-agent` 的 **M6f-3：`internal/fs` 安全文件操作接缝 + 实现 + `fs_*` 工具 + `fs/*` 事件 + config + 组合根接线 + 单元测试**。这是 M6f 的第三派发（安全 fs 封装；路径约束在允许根内）。零新依赖（os 标准库）。你是实施会话。

**直接开工，不要做任何前置检查**。你的主要输入是 `D:\dev-projects\Agent\shutu-agent\docs\dispatch-m6f-3.md`（本文件即主契约），**先完整读它**，然后立即用 write 工具创建/修改文件。

**读这些（按需精读片段，不要通读）**：
1. `D:\dev-projects\Agent\shutu-agent\docs\decisions\2026-08-19-m6-agent-full.md` —— M6 主 ADR，重点读 M6f 行（fs 安全封装）。
2. `internal/schedule/service.go` + `internal/code/service.go` —— 接缝模板（Provider/Engine + 哨兵错误 + Close 幂等）。
3. `internal/session/session.go` —— 各能力事件的 log-only 模式（模板）。
4. `internal/config/config.go` —— 各段模式 + applyDefaults 白名单。
5. `cmd/pa/*.go` —— register* 组合根模式、工具注册、onEvent sink、/help 状态行。
6. 参考（只借鉴思路，不精读）：`D:\dev-projects\Agent\deepseek-harness\packages\fs\fs\src\types.ts`、`fs-sandbox\src\containment.ts`、`tool-fs\src\read.ts`/`write.ts`。

**实现内容**：
1. **`internal/fs` 接缝（`service.go`）**：
   ```go
   type Entry struct { Name, Path string; IsDir bool; Size int64 }
   type FileService interface {
       // Read 读取允许根内文件；路径越界或不存在报错；可选 size 上限。
       Read(ctx, path string, maxSize int) (string, error)
       // Write 写入允许根内文件（创建/覆盖）；路径越界报错；父目录缺失时创建。
       Write(ctx, path, content string) error
       // List 列出允许根内目录（非递归）；路径越界或不存在报错。
       List(ctx, dir string) ([]Entry, error)
       // Root 返回允许根（配置的 fs.root，默认 <项目>）。
       Root() string
       Close() error
   }
   ```
   - **安全边界**：所有路径经 `filepath.Clean` + 前缀校验（必须在允许根内；`..` 逃逸拒绝）；`List` 非递归；`Read` 有 size 上限（默认 1MB，防读大文件爆上下文）。
   - 默认实现：`localFS`（`NewLocalFS(root string)`，零依赖 os/filepath）。
   - Close 幂等（本地实现无资源，标记 closed）。
2. **事件（`internal/session/session.go` + 测试）**：新增 `EventFsRead/Write/List`（`fs/read|write|list`）+ `NewFsRead(path string, size int) any`、`NewFsWrite(path string) any`、`NewFsList(dir string, count int) any`。**log-only**：DeriveHistory 不派生。
3. **config（`internal/config` + config.yaml）**：`FsConfig{Enabled bool, Root string}`（yaml: `enabled/root`）；默认 `enabled:false / root:""（默认 <项目>）`。**enabled 时自动白名单 `fs_*` 工具**。
4. **fs_* 工具（`internal/fs/tools.go` + 测试）**：`NewFsTools(f FileService, onEvent func(typ string, data any))` 返回结构化 tools.Tool 集合（不 import tools 包，D2）：
   - `fs_read(path)`：读文件；D7 schema（additionalProperties:false）；落 `fs/read`。
   - `fs_write(path, content)`：写文件；落 `fs/write`。
   - `fs_list(dir)`：列目录；落 `fs/list`。
   - 事件经 onEvent sink（串行工具路径）；路径越界/不存在返回错误消息（非 panic）。
5. **组合根（cmd/pa `registerFs()`）**：`fs.enabled` 时建 localFS（root 默认 <项目>）+ 注册 fs_* 工具（白名单）+ 事件 sink；disabled 零操作（D10）。`main.go` 调用 + `app.fs` 字段 + deferred Close + /help 状态行。**接线位置在工具注册层，不改 loop（D4）**；串行路径（D5）。

**纪律**：**日志仍追加式（D1）**；不改 loop turn/step（D4）；串行路径（D5）；零新依赖（os 标准库）；CGO-free；原有测试全绿。**不要动**：loop.go（只读）、compaction、subagent、skill、kb、store、schedule、plan、spill、interact、code、mcp 包（只读参考）。**不要做**：M6f 之外的段、KB 补全、shell/terminal（M3 已有 run_command）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）。每次 Go 命令这样跑（用 pwsh）：
`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\shutu-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\shutu-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\shutu-agent\.gocache'; & 'C:\Program Files\Go\bin\go.exe' test ./...`
git 提交：`git -C D:\dev-projects\Agent\shutu-agent -c user.name='Personal Agent' -c user.email='dev@shutu-agent.local' commit -m "M6f-3: <what>"`。不要提交 pa.exe/data/缓存。

**上下文管理（关键）**：**分阶段提交**——① service.go 接缝 + local.go → ② session 事件 → ③ config → ④ tools + 组合根，每阶段一次 commit（信息含 "M6f-3"）。不要通读任何参考库。报告只列文件名+一句话。

**自测**（全部通过再报告）：vet/test/build 三命令全绿。新增测试至少覆盖：Read/Write/List、路径越界（.. 逃逸拒绝）、Read 大小上限、父目录缺失创建、事件追加/重放/不派生、config 缺省 + 白名单、fs_read/write/list（D7 + 事件）、enabled=false 不注册。

**完成报告**：改动文件清单、实现决策（路径安全边界、Read 上限、Root 默认）、测试结果、提交 hash 列表、对 M6 主 ADR 的更新说明（如有）。提交后报告即交接，不要等待确认。
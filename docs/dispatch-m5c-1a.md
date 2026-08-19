# M5c-1a 实施派发消息（控制会话 → 实施会话）——session 折叠规则改造

> 状态：已派发 2026-08-19（M5c 第一半因任务过大拆为 1a/1b 顺序派发；本文件为 1a：折叠规则改造）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M5c-1a：session 折叠规则改造（`derive()` + `NewUserMessageReplace`）+ 单元测试**。这是 M5c 的一个**小**子任务（M5c-1b 另一个会话做 compaction 接缝）。你是实施会话。

**必读（先读这些，不要通读参考源码）**：
1. `D:\dev-projects\Agent\personal-agent\docs\dispatch-m5c-1.md` —— 你的主契约，重点读「实现内容」第 1 条（折叠规则改造）。**其余条目是 M5c-1b 的事，只读参考。**
2. `D:\dev-projects\Agent\personal-agent\internal\session\session.go` —— **`derive()` 纯函数（第 158–201 行）与 `userMessageData`（第 207–209 行）是改造点**。
3. `D:\dev-projects\Agent\personal-agent\internal\session\session_test.go` —— derive 相关测试模式。
4. `Agent.md` 第 10 节 D1–D10 纪律。

**实现内容**（严格按契约，只做这一条）：
1. `userMessageData` 增加可选字段 `SurfaceOp *SurfaceReplace`，`SurfaceReplace struct{ Op string; Start, End int64 }`（`Op == "replace"`）。
2. 新增 `NewUserMessageReplace(text string, start, end int64)` 构造（返回 Event 类型 `EventUserMessage`，Data 含 `surfaceOp:{op:"replace",start,end}`）。**`NewUserMessage` 原签名完全不变**（loop 调用方不改）。
3. **`derive()` 改造**：遇到带 `surfaceOp.replace` 的 `user/message` 时，记录被遮蔽范围（Start/End seq），把该消息 Text（摘要）追加到结果，并跳过后续 `seq ∈ [start, end]` 的事件（seq 单调递增，按 seq 比较跳过，seq > end 恢复）。**无 replace 标记时行为完全不变**（原测试全绿）。
4. 测试（`session_test.go` 新增）：带 replace 的 user/message 折叠后 = 摘要 + 未遮蔽尾部（被遮蔽 seq 的事件不出现）；无 replace 行为不变；遮蔽范围跨越 user/assistant/tool 混合事件；空摘要文本消息也被保留；JSON 往返（surfaceOp 字段正确序列化/反序列化）。

**纪律**：**日志仍追加式（D1）**——本任务不删除、不改写任何已落事件，只加新事件类型与折叠规则；`NewUserMessage` 向后兼容；零新依赖；CGO-free；原有测试全绿（尤其 derive 测试）。**不要动**：loop、cmd/pa、config、jobs、subagent、tools、kb、store 包（只读参考）。**不要做**：/compact 命令、compaction/* 事件、config、PreStep 接线、compaction 包（M5c-1b/1c-2）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'`。用 pwsh 执行命令。git 提交用 `git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "..."`。不要提交 `pa.exe`、`data/`、缓存目录。

**上下文管理（关键）**：这是**小任务**——只读 session.go 的 derive/userMessageData 相关行与 session_test.go 的 derive 测试；不要通读整个文件或参考库。完成即一次性提交（信息含 "M5c-1a"）。

**自测**（全部通过再报告）：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖：带 replace 摘要 + 遮蔽 seq 跳过、无 replace 不变、混合事件、空摘要保留、JSON 往返。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash。提交后报告，不要等待确认——报告即交接。

# M5d-2 实施派发消息（控制会话 → 实施会话）——skill_load 工具 + 事件 + config + PreStep 目录注入

> 状态：待 M5d-1 验收后派发 2026-08-19（M5 拆四段：M5a ✅ → M5b ✅ → M5c ✅ → M5d 技能；M5d 拆为 1/2 顺序派发；本文件为 2：工具 + 事件 + config + 接线）· 用法：M5d-1 验收通过后，把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M5d-2：`skill_load` 工具 + `skill/*` 事件 + config + PreStep 目录注入接线 + 单元测试**。这是 M5d 的第二半（第一半 M5d-1 已做 `internal/skill` 注册表 + 文件系统 Provider，你**依赖它们**）。你是实施会话。

**必读（先读这些，不要通读参考源码）**：
1. `D:\dev-projects\Agent\personal-agent\docs\dispatch-m5d-2.md` —— 本文件即主契约，自包含实现范围、签名、约束、自测标准（下文）。
2. `D:\dev-projects\Agent\personal-agent\docs\dispatch-m5d.md` —— 背景契约，重点读「M5d 范围」第 1 条的**目录注入与 skill_load 工具设计**、第 2（事件）、3（config）条与「约束」「决策记录」节。
3. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-18-m5-agent-core.md` 的「决策 ④」。
4. 现有代码（按需精读片段）：
   - `internal/skill/service.go` + `filesystem.go`（M5d-1 已做：Registry/Provider/Candidate/Definition + 文件系统 Provider）。
   - `internal/session/session.go` —— job/subagent/compaction 事件的 log-only 模式（模板）。
   - `internal/loop/loop.go` —— `Config.PreStep []PreStepInjector{Name, Inject func(ctx, userText string) []llm.Message}`（逐 turn 调用、per-injector 4000 rune 上限、panic fail-open）。**接线方不修改 loop.go**。
   - `internal/config/config.go` —— Jobs/Subagent/Compaction 段模式。
   - `cmd/pa/*.go` —— registerKB/registerJobs/registerSubagent/registerCompaction 组合根模式、命令注册、onEvent sink、工具注册（registerJobs 注册 job_* 工具 + applyDefaults 白名单模式）。
   - `internal/tools/` —— tools.Tool 结构化接口与 D7 校验（registerJobs 的工具注册 + D7 schema 模式）。
5. `Agent.md` 第 10 节 D1–D10 纪律 + design.md §3 事件词汇表（skill/*）。

**实现内容**（严格按 dispatch-m5d.md）：
1. **事件（`internal/session/session.go` + 测试）**：新增 `EventSkillCatalog = "skill/catalog"`、`EventSkillLoad = "skill/load"` + `NewSkillCatalog(entryCount int, version string) any`、`NewSkillLoad(name, source string, summary string) any`（正文摘要 200-rune 有界）。**log-only**：DeriveHistory 不派生（与 job/subagent/compaction 一致）。
2. **config（`internal/config` + config.yaml）**：`SkillConfig{Enabled bool, Dirs []string, CatalogMaxChars int, BodyMaxChars int}`（yaml: `enabled/dirs/catalog_max_chars/body_max_chars`）；默认 `enabled:false / catalog_max_chars:500 / body_max_chars:8000`（Dirs 空）。applyDefaults：`catalog_max_chars<=0` → 500；`body_max_chars<=0` → 8000；校验非负。**enabled 时自动白名单 `skill_load` 工具**（与 job_*/subagent_* 同模式）。
3. **skill_load 工具（`internal/skill/tools.go` + 测试，或 cmd/pa 侧）**：`NewSkillTools(reg Registry, bodyMaxChars int, onEvent func(typ string, data any))` 返回含 `SkillLoad` 工具的结构化 tools.Tool（不 import tools 包，D2）：
   - `skill_load(name)`：D7 schema（additionalProperties:false）；校验 kebab-case（`^[a-z0-9]+(-[a-z0-9]+)*$`）→ `reg.Get(name)` → 加载完整正文返回模型（`<skill_content>` 包裹，`body_max_chars` 截断，Unicode 安全）；落 `skill/load` 事件（log-only，经 onEvent sink）+ 结果经 `tool/result`（D3 由工具层落）。
   - 未知技能返回错误消息（工具层处理，非 panic）。
4. **PreStep 目录注入（cmd/pa 接线）**：`skill.enabled` 时向 loop `Config.PreStep` 追加注入器（Name "skill"，在 compaction 之后）：每 turn 前把技能**目录**（排序后的 `name + description`，`catalog_max_chars` 有界，不塞正文/路径/来源）注入为上下文消息，并落 `skill/catalog` 事件（log-only）。目录变更由组合根按需重读（下一次 pre-step 重取；不引入文件监视）。**所有 Append 都在串行 PreStep/工具路径（D5）**。
5. **组合根（cmd/pa `registerSkills()`）**：`skill.enabled` 时创建文件系统 Provider（ProjectRoot/UserHome/Dirs from config）+ Registry + 注册 provider + 注册 skill_load 工具（白名单）+ PreStep 注入器；disabled 零操作（D10，镜像 registerKB/Jobs/Subagent/Compaction）。`main.go` 调用（在 registerCompaction 后）+ `app.skills` 字段。

**纪律**：技能是本地可信文件，加载后作为模型指令输入，**不执行**；**不改 loop turn/step（D4）**——只通过 PreStep 扩展点接线，不动 loop.go；主循环串行（D5）；日志仍追加式（D1）；零新依赖；CGO-free；原有测试全绿。**不要动**：`internal/skill/service.go`/`filesystem.go`（M5d-1 已验收，只读）、`internal/compaction`、loop.go（只读）、jobs、subagent、kb、store 包（只读参考）。**不要做**：KB 补全、scope 分层、远程 Provider、文件监视、打包 badge 技能。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'`。用 pwsh 执行命令。git 提交用 `git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "..."`。不要提交 `pa.exe`、`data/`、缓存目录。

**上下文管理（关键）**：**分阶段提交**（session 事件一次 → config 一次 → skill_load 工具一次 → PreStep + 组合根接线一次，信息含 "M5d-2"）；只按需精读片段，不要通读参考库；报告只列文件名 + 一句话。

**自测**（全部通过再报告）：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试至少覆盖 dispatch-m5d.md「自测」段中属本任务的部分：目录注入有界（catalog_max_chars 截断）+ skill/catalog 事件、skill_load 工具（kebab-case 校验、正文长度上限、tool/result）、skill/* 事件可落日志、skill 默认关闭（enabled=false 不初始化/不注册工具）。

**完成报告**：改动文件清单、实现决策（目录注入器如何接、skill_load 如何落事件）、测试结果、提交 hash 列表、对 M5 主 ADR 的更新说明（如有）。提交后报告，不要等待确认——报告即交接。

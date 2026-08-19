# M5d-1 实施派发消息（控制会话 → 实施会话）——skill 注册表 + 文件系统 Provider

> 状态：已派发 2026-08-19（M5 拆四段：M5a ✅ → M5b ✅ → M5c ✅ → M5d 技能；M5d 拆为 1/2 顺序派发；本文件为 1：注册表 + 文件系统 Provider）· 用法：把下文整段粘贴给新开的实施会话。

---

你的任务是实现 Go 项目 `D:\dev-projects\Agent\personal-agent` 的 **M5d-1：`internal/skill` 注册表接缝（多 Provider）+ 文件系统 Provider（默认）+ 单元测试**。这是 M5d 的第一半（第二半 M5d-2 做 skill_load 工具 + skill/* 事件 + config + PreStep 目录注入接线，依赖你的注册表）。你是实施会话。

**必读（先读这些，不要通读参考源码）**：
1. `D:\dev-projects\Agent\personal-agent\docs\dispatch-m5d.md` —— 背景契约，重点读「M5d 范围」第 1 条的**注册表 + 文件系统 Provider 设计**（Candidate/Definition/Provider/Registry 签名、发现优先级、技能身份、frontmatter、同名裁决）与「约束」「决策记录」节。**第 2（事件）、3（config）、目录注入、skill_load 工具是 M5d-2 的事，只读参考。**
2. `D:\dev-projects\Agent\personal-agent\docs\decisions\2026-08-18-m5-agent-core.md` 的「决策 ④」。
3. 参考源码（**只借鉴思路与契约，不照搬 TS，不精读**）：`D:\dev-projects\Agent\deepseek-harness\packages\skill\` 的 `skill/`（注册表 `src/index.ts`）、`skill-filesystem/`（本地发现）。
4. `Agent.md` 第 10 节 D1–D10 纪律。

**实现内容**（严格按 dispatch-m5d.md）：
1. **`internal/skill` 包——注册表接缝（`service.go`）**：
   ```go
   type Candidate struct {
       Name        string   // kebab-case（^[a-z0-9]+(-[a-z0-9]+)*$）
       Description string
       Source      string   // project-dsh | project-agents | user-dsh | custom | ...
       Rank        int      // 低 rank 优先（同名裁决）
       Path        string   // 绝对路径（文件系统 provider）
   }
   type Definition struct {
       Name, Description, Content string
       Source, Path string
       ModelInvocable, UserInvocable bool
   }
   type Provider interface {
       Name() string
       List(ctx context.Context) ([]Candidate, error)
       Get(ctx context.Context, c Candidate) (*Definition, error)
   }
   type Registry interface {
       RegisterProvider(p Provider) error
       List(ctx context.Context) ([]Candidate, error)   // 合并、按 rank 裁决、按 name 排序
       Get(ctx context.Context, name string) (*Definition, error)
       Close() error
   }
   ```
   - Registry 实现：多 Provider 合并、同名按 rank 裁决（低 rank 优先，同 rank 按 provider 注册序再本地序）、`Get` 加载时若 name 与 candidate 不再匹配则拒绝。
2. **文件系统 Provider（默认，`filesystem.go`）**：`NewFilesystem(FSOpts{ProjectRoot, UserHome, Dirs []string})`：
   - 扫描根按 rank：100 `project-dsh` `<projectRoot>/.dsh/skills`；200 `project-agents` `<projectRoot>/.agents/skills`；300 `custom` `FSOpts.Dirs`；400 `user-dsh` `<userHome>/.dsh/skills`。projectRoot = 最近的含 `.git` 祖先，无则 cwd。
   - 技能身份：kebab-case 名；目录束 `<name>/SKILL.md` 或平铺 `<name>.md`；**不递归发现**。
   - frontmatter 解析：支持 `disable-model-invocation` / `user-invocable`（缺省均 true）。描述取 frontmatter 的 description 或正文首行。
   - 同名裁决：低 rank 优先（Registry 层做；Provider 只出 candidate）。
3. **测试（`internal/skill/service_test.go` + `filesystem_test.go`）**：文件系统发现（目录束 + 平铺 + 非递归 + frontmatter 解析 + rank 顺序）、同名 rank 裁决、`Get` 加载完整正文 + name 失配拒绝、Registry 合并排序。

**纪律**：技能是本地可信文件，**不执行**；零新依赖；CGO-free；原有测试全绿。**不要动**：`internal/compaction`、loop、cmd/pa、config、jobs、subagent、kb、store、tools、session 包（只读参考）。**不要做**：skill_load 工具、skill/* 事件、config、PreStep 目录注入接线（M5d-2）。

**环境（重要）**：Go 在 `C:\Program Files\Go\bin\go.exe`（不在 PATH）；每次 Go 命令设 `$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'`。用 pwsh 执行命令。git 提交用 `git -C D:\dev-projects\Agent\personal-agent -c user.name='Personal Agent' -c user.email='dev@personal-agent.local' commit -m "..."`。不要提交 `pa.exe`、`data/`、缓存目录。

**上下文管理（关键）**：**分阶段提交**（service.go 注册表一次 → filesystem.go Provider 一次 → 测试一次，信息含 "M5d-1"）；只按需精读片段，不要通读参考库；报告只列文件名 + 一句话。

**自测**（全部通过再报告）：`go vet ./...`、`go test ./...`、`go build ./...`。新增测试覆盖 dispatch-m5d.md「自测」段中属本任务的部分：文件系统发现（目录束 + 平铺 + 非递归 + frontmatter 解析）、同名 rank 裁决、`Get` 加载完整正文 + name 失配拒绝。

**完成报告**：改动文件清单、实现决策、测试结果、提交 hash 列表、对 M5 主 ADR 的更新说明（如有）。提交后报告，不要等待确认——报告即交接。

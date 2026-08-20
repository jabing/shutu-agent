# Eval-2b 派发：subagent 验收标准（StartRequest.AcceptanceCriteria + spawn 注入 + subagent_spawn）

> 评测接缝 ADR `docs/decisions/2026-08-20-eval-seam.md` D-EVAL-4。本文件是 **Eval-2b** 契约：subagent 域支持验收标准——StartRequest 带 AcceptanceCriteria、SpawnProvider 注入子代理 prompt 尾部"验收标准（交付自检）"段、subagent_spawn 工具透传。前置：Eval-1/Eval-2a 已交付（本段不依赖）。

## 纪律

- 零新依赖、CGO-free；只改 subagent 域相关文件；gofmt；不改 loop。
- 提交 1 个：`Eval-2b: subagent 验收标准（StartRequest.AcceptanceCriteria + spawn prompt 注入 + subagent_spawn）`

## 变更清单（精确）

### 1. internal/subagent/service.go — StartRequest 加字段
`StartRequest` struct（当前 Label/Prompt/ParentSessionID/MaxDepth/ToolFilter/Persona）加：
```go
	// AcceptanceCriteria is the optional eval acceptance criteria list (ADR
	// D-EVAL-4). The provider injects it into the child's prompt as a
	// "验收标准（交付自检）" section so the child self-checks its deliverable.
	AcceptanceCriteria []string
```

### 2. internal/subagent/spawn.go — 注入段
- Start 内、`req.Prompt == ""` 校验之后、构造 runCtx 之前加：
```go
	req.Prompt = withAcceptance(req.Prompt, req.AcceptanceCriteria)
```
- 加辅助（spawn.go 底部）：
```go
// acceptanceSection is the eval self-check section appended to a child prompt
// when acceptance criteria are given (ADR D-EVAL-4).
const acceptanceSection = "\n\n## 验收标准（交付自检）\n你的交付必须满足以下验收标准，完成后逐条自检，并在最终回复中逐条说明每条的满足情况：\n"

// withAcceptance appends the acceptance criteria section to prompt when
// criteria are non-empty; otherwise it returns prompt unchanged.
func withAcceptance(prompt string, criteria []string) string {
	if len(criteria) == 0 {
		return prompt
	}
	var sb strings.Builder
	sb.WriteString(prompt)
	sb.WriteString(acceptanceSection)
	for _, c := range criteria {
		if strings.TrimSpace(c) == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(strings.TrimSpace(c))
		sb.WriteByte('\n')
	}
	return sb.String()
}
```
（spawn.go 需 import "strings"——检查现有 import，无则加。）

### 3. internal/subagent/tools.go — subagent_spawn 透传
- Schema 加：
```go
			"acceptance_criteria": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "minLength": 1},
				"description": "optional acceptance criteria the deliverable must satisfy (eval); injected into the subagent prompt for self-check",
			},
```
- Execute：`a` struct 加 `AcceptanceCriteria []string \`json:"acceptance_criteria"\``；StartRequest 加 `AcceptanceCriteria: a.AcceptanceCriteria`。

### 4. 测试（internal/subagent/spawn_test.go + tools_test.go）
- `TestWithAcceptance`（纯函数）：空 criteria → 原样返回；非空 → 返回含 "验收标准（交付自检）" 段且逐条含 "- <criterion>"；空串条目被跳过；原 prompt 保留。
- `TestSpawnInjectsAcceptance`（集成，照 TestSpawnFullRound 模式）：scriptedLLM + NewSpawnProvider(Deps{Log, LLM, Tools, Prompt, Model})；Start(StartRequest{Prompt:"do X", AcceptanceCriteria: []string{"contains:输出含报告", "llm:结论合理"}})；Result 完成（steps 给一个 finish 事件）；断言 `model.calls[0]` 的 messages[0].Text() 含 "验收标准" 与两条 criterion 文本。
- `TestSpawnNoCriteriaNoInjection`：AcceptanceCriteria 空 → calls[0] messages[0].Text() 恰为原始 Prompt（不含 "验收标准"）。
- tools_test.go：subagent_spawn schema 含 `acceptance_criteria` 键（若已有 spawn 工具测试，补断言或最小用例）。

## 验证

`go build ./...` + `go test -count=1 ./internal/subagent/` 全 PASS 后提交。

## 环境

- Go：`C:\Program Files\Go\bin\go.exe`；env：`$env:GOTELEMETRY='off'; $env:GOFLAGS='-mod=mod'; $env:GOMODCACHE='D:\dev-projects\Agent\personal-agent\.gomodcache'; $env:GOPATH='D:\dev-projects\Agent\personal-agent\.gopath'; $env:GOCACHE='D:\dev-projects\Agent\personal-agent\.gocache'`；提交身份 `-c user.name='Personal Agent' -c user.email='dev@personal-agent.local'`；pwsh，workdir=`D:\dev-projects\Agent\personal-agent`。

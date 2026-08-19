# 知识库实测指引（M4 验收后 · 手动体验）

> 目的：在真实 REPL 里体验 M4 知识库的**检索 / 主动召回 / 提取回写**三个能力。
> 前提：已设置 `$env:DEEPSEEK_API_KEY`。所有命令在 `D:\dev-projects\Agent\personal-agent` 下执行。

## 0. 启用（用独立测试配置，不影响默认 config.yaml）

配置文件已就绪：`config.kb-test.yaml`（`kb.enabled: true`，独立库 `data/kb/knowledge.sqlite`）。一键脚本已就绪：`run-kb-test.ps1`（自动检查 API Key → 缺 pa.exe 则构建 → 用测试配置启动）。

```powershell
# 方式 A：一键脚本（推荐）
.\run-kb-test.ps1

# 方式 B：手动
$env:DEEPSEEK_API_KEY = "sk-..."          # 环境变量，绝不写进配置
go build -o pa.exe ./cmd/pa               # 缺 pa.exe 时先构建（构建产物不入 git）
.\pa.exe --config config.kb-test.yaml
```

启动后应看到 `/help` 里有 `kb-status`/`kb-reindex`，且提示 `knowledge base: enabled`。

## 1. 显式写入（kb_add 工具）

在 REPL 里让模型显式记住一些东西（会触发 `kb_add` 工具）：

```
请记住：我周末不工作，工作日早上 9 点到下午 6 点在线。
请记住：我的个人项目叫 Personal Agent，用 Go 编写，参考 DeepSeek Harness 架构。
```

观察：模型应调用 `kb_add` 写入，回答确认已记录。

## 2. 检索（kb_search 工具）

用一条**不相关的**新问题触发模型主动检索（它会看到系统提示词里的知识库目录，知道该用 kb_search）：

```
我刚才让你记住的周末安排是什么？
```

观察：模型应调用 `kb_search`，引用带来源的片段作答。

## 3. 主动召回（kb/recall）

新开一轮，输入一个自然语言问题——只要 `recall_limit: 3` 生效，每轮开始时 REPL 会主动召回相关片段注入上下文（片段以 `Relevant knowledge snippets were proactively retrieved...` 开头）：

```
我在周末一般做什么？
```

观察：回答前注入的召回片段让模型直接基于库内容作答，无需调用工具。

## 4. 提取回写（kb/extract）

**不手动告诉模型**，让它从一次"决策式"对话里自然产生知识，例如：

```
以后我写 Go 工具时，默认都用 pure Go、不开 CGO。
```

回答后，提取回写会自动运行（每轮结束后）。随后：

```
/kb-status
```

观察：`recent writes` 出现一条 `procedure` 或 `fact` 类型的自动提取条目（来源是 `session:...`，不是 `manual`）——这就是"对话沉淀知识"。

## 5. 验证事件日志（D3）

用任意会话继续聊几句后退出，SQLite 会话库 `data/pa.db` 里应能查到三类 kb 事件：

```powershell
# 需要 sqlite3 命令行；没有就跳过，靠 /kb-status 已能间接确认
sqlite3 data/pa.db "SELECT type, count(*) FROM events GROUP BY type ORDER BY type;"
# 期望出现 kb/recall、kb/add、kb/extract
```

## 6. 清理 / 恢复

- 实测完恢复默认：直接退出 REPL 即可（默认 `config.yaml` 仍是 `kb.enabled: false`，未受影响）。
- 清空知识库重来：删掉 `data/kb/` 目录即可（下次启动自动重建）。
- 重建 FTS 索引（如怀疑索引漂移）：REPL 内 `/kb-reindex`。

## 常见问题

| 现象 | 原因 / 处理 |
|---|---|
| `/kb-status` 提示 `kb: disabled` | 没用 `--config config.kb-test.yaml` 启动 |
| 模型不主动调 kb_search | 提一个明确依赖已存知识的问句（如"我让你记住的 X 是什么"）；若仍不用，可把 `catalog` 提示词改进后在 ADR 记录 |
| 提取没产生条目 | 该轮回答确无可复用知识是正常的（`skipped`）；`kb.extraction` 缺省 true 已开 |
| 中文召回命中差 | 二元组 LIKE 兜底对两字词/短语有效，超短查询会多命中——个人规模可接受（ADR 已记录） |

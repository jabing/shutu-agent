# 一键启动知识库实测（kb-test-guide.md 的配套脚本）
# 用法：右键"使用 PowerShell 运行"，或在 PowerShell 里执行 .\run-kb-test.ps1
# 功能：检查 DEEPSEEK_API_KEY → 若缺 pa.exe 则构建 → 用 config.kb-test.yaml 启动 REPL

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

# 1. 环境变量检查（API Key 只走环境变量，绝不写入配置文件）
if (-not $env:DEEPSEEK_API_KEY) {
    Write-Host "缺少环境变量 DEEPSEEK_API_KEY。先设置它（任选一种）：" -ForegroundColor Yellow
    Write-Host "  当前会话：`$env:DEEPSEEK_API_KEY = `"sk-...`"" -ForegroundColor Cyan
    Write-Host "  永久生效：setx DEEPSEEK_API_KEY `"sk-...`"（新开终端生效）" -ForegroundColor Cyan
    exit 1
}

# 2. 确保 pa.exe 存在（构建产物不入 git，删过就要重建）
if (-not (Test-Path .\pa.exe)) {
    Write-Host "未找到 pa.exe，正在构建..." -ForegroundColor Yellow
    go build -o pa.exe ./cmd/pa
    if ($LASTEXITCODE -ne 0) { Write-Host "构建失败" -ForegroundColor Red; exit 1 }
}

# 3. 启动 REPL（知识库已启用，默认 config.yaml 不受影响）
Write-Host "启动知识库实测 REPL（config.kb-test.yaml，kb.enabled=true）..." -ForegroundColor Green
.\pa.exe --config config.kb-test.yaml

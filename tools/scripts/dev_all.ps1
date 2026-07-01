# Pandora 一键开发环境(基础设施 + 全部业务服务)
#
# 这是给"UE 联调"用的一条命令:先拉起 docker 基础设施(MySQL/Redis/Kafka/etcd/Envoy),
# 等容器 healthy 后,再按依赖顺序拉起 Go 业务服务。
#
# 用法:
#   # 起全部业务服务(默认)
#   pwsh tools/scripts/dev_all.ps1
#
#   # 全起但排除某个服务(留给 VS Code 断点调试)
#   pwsh tools/scripts/dev_all.ps1 -Exclude team
#
#   # 全停(基础设施 + 业务服务)
#   pwsh tools/scripts/dev_all.ps1 -Down

[CmdletBinding()]
param(
    [string[]]$Exclude = @(),
    [switch]$Pull,
    [switch]$Down
)

$ErrorActionPreference = 'Stop'
$ScriptDir = $PSScriptRoot

if ($Down) {
    Write-Host "===== 停止业务服务 =====" -ForegroundColor Cyan
    & "$ScriptDir/run_services.ps1" -Action down
    Write-Host ""
    Write-Host "===== 停止基础设施 =====" -ForegroundColor Cyan
    & "$ScriptDir/dev_down.ps1"
    exit $LASTEXITCODE
}

# 1) 基础设施
Write-Host "===== [1/2] 基础设施 =====" -ForegroundColor Cyan
if ($Pull) { & "$ScriptDir/dev_up.ps1" -Pull } else { & "$ScriptDir/dev_up.ps1" }
if ($LASTEXITCODE -ne 0) {
    Write-Host "[ERR] 基础设施启动失败,中止" -ForegroundColor Red
    exit 1
}

# 2) 业务服务
Write-Host ""
Write-Host "===== [2/2] 业务服务 =====" -ForegroundColor Cyan
& "$ScriptDir/run_services.ps1" -Exclude $Exclude
exit $LASTEXITCODE

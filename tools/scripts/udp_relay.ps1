# Pandora 本地 Agones UDP 回程中继(minikube docker-driver 专用)
#
# 用法:
#   pwsh tools/scripts/udp_relay.ps1                       # 自动解析 minikube ip 作为转发目标
#   pwsh tools/scripts/udp_relay.ps1 -TargetHost 192.168.49.2
#   pwsh tools/scripts/udp_relay.ps1 -PortRange 7000-8000
#
# 背景(为什么需要它):
#   minikube 用 docker driver 时,GameServer Pod 的 status.address 是集群内网 IP(10.x / node IP),
#   Windows 客户端直连不到。allocator 侧用 advertise_host=127.0.0.1 把返回地址改写成本机回环,
#   再由本中继把 127.0.0.1:<port> 的 UDP 流量转发到 minikube 节点的同端口(NodePort -> Pod)。
#   端口范围对齐 Agones dynamic port 默认 [7000,8000]。
#
#   client --UDP--> 127.0.0.1:<port>  --[本脚本: tools/udp-relay]-->  <minikube ip>:<port> --> GameServer
#
# 注意:
#   - 仅本地 dev 联调使用,生产/真集群用 GameServer status.address 直连,不需要本中继。
#   - 该工具是纯标准库零依赖程序,直接 go run,不进 go.work。
#   - 监听 7000-8000 共 1001 个 UDP 端口,属本地 dev 行为,资源占用可接受。

param(
    [string]$TargetHost = "",
    [string]$PortRange  = "7000-8000"
)

$ErrorActionPreference = "Stop"

$ProjectRoot = Resolve-Path "$PSScriptRoot/../.."
$RelayDir    = Join-Path $ProjectRoot "tools/udp-relay"

Write-Host "===== Pandora UDP relay (local Agones) =====" -ForegroundColor Cyan

if (-not (Test-Path (Join-Path $RelayDir "main.go"))) {
    Write-Host "[ERR] relay source not found: $RelayDir/main.go" -ForegroundColor Red
    exit 1
}

# 未显式指定时,自动解析 minikube 节点 IP 作为转发目标(避免硬编码默认值与实际不符)。
if ([string]::IsNullOrWhiteSpace($TargetHost)) {
    Write-Host "[1/2] Resolving minikube ip..." -ForegroundColor Yellow
    try {
        $TargetHost = (& minikube ip 2>$null | Out-String).Trim()
    } catch {
        $TargetHost = ""
    }
    if ([string]::IsNullOrWhiteSpace($TargetHost)) {
        Write-Host "[ERR] 无法自动解析 minikube ip。请确认 minikube 已启动,或用 -TargetHost 显式指定。" -ForegroundColor Red
        exit 1
    }
    Write-Host "      minikube ip = $TargetHost" -ForegroundColor Green
} else {
    Write-Host "[1/2] Using target host: $TargetHost" -ForegroundColor Yellow
}

Write-Host "[2/2] Starting relay -> ${TargetHost} ports ${PortRange} (Ctrl+C 停止)" -ForegroundColor Yellow
Write-Host ""

$env:TARGET_HOST = $TargetHost
$env:PORT_RANGE  = $PortRange

Push-Location $RelayDir
try {
    go run main.go
} finally {
    Pop-Location
}

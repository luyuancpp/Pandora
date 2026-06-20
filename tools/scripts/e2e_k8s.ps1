# Pandora 本地「线上等价」真 DS 闭环一键编排(minikube + Agones,无 mock)
#
# 串起 -Mode k8s 之后剩余的手工步骤,让 codex 一条命令把真 DS 链路准备好:
#   1) 校验 minikube / Agones / Fleet / 后端 allocator 就绪
#   2) 把两个真 UE Linux DS 镜像(精确 tag,从 Fleet yaml 动态解析)load 进 minikube
#   3) 起宿主 Envoy 桥接(kubectl port-forward 所有 envoy upstream + docker envoy :8443/:8444)
#   4) 等 pandora-battle / pandora-hub Fleet Ready
#   5) (可选)后台拉起 UDP 回程中继(docker driver 下客户端连 DS 必需)
#   6) 打印端到端验收清单(用真 UE 客户端验:登录→hub→战斗→结算回 hub)
#
# 前置(由 start.ps1 -Mode k8s 完成):minikube 起、Agones 装好、RBAC/Fleet apply、16 个后端服务部署。
#   pwsh tools/scripts/start.ps1 -Mode k8s
#   pwsh tools/scripts/e2e_k8s.ps1
#
# 用法:
#   pwsh tools/scripts/e2e_k8s.ps1                 # 校验 + load 镜像 + 等 Fleet + 起中继(后台)
#   pwsh tools/scripts/e2e_k8s.ps1 -NoRelay        # 不自动起中继(自己另开终端跑 udp_relay.ps1)
#   pwsh tools/scripts/e2e_k8s.ps1 -SkipImageLoad  # 镜像已 load 过,跳过
#   pwsh tools/scripts/e2e_k8s.ps1 -TimeoutSec 300 # 等 Fleet Ready 的超时(默认 240s)
#   pwsh tools/scripts/e2e_k8s.ps1 -BridgeForce    # 500xx 端口被本地/compose 旧服务占用时,杀掉后重建 port-forward

[CmdletBinding()]
param(
    [switch]$NoRelay,        # 不自动后台起 UDP 中继
    [switch]$SkipImageLoad,  # 跳过 minikube image load
    [switch]$BridgeForce,    # 端口被非 bridge 进程占用时,杀掉占用者后重建 port-forward
    [int]$TimeoutSec = 240   # 等 Fleet Ready 超时秒
)

$ErrorActionPreference = 'Stop'
$ScriptDir   = $PSScriptRoot
$ProjectRoot = (Resolve-Path "$ScriptDir/../..").Path
$AgonesDir   = Join-Path $ProjectRoot 'deploy/k8s/agones'
$K8sNamespace = 'pandora'   # 后端服务 + allocator 所在 ns
$FleetNamespace = 'default' # Fleet / GameServer 所在 ns(见 20/30-fleet-*.yaml)

function Write-Info($m) { Write-Host "[INFO] $m" -ForegroundColor Cyan }
function Write-Ok($m)   { Write-Host "[ OK ] $m" -ForegroundColor Green }
function Write-Warn($m) { Write-Host "[WARN] $m" -ForegroundColor Yellow }
function Write-Err($m)  { Write-Host "[ERR ] $m" -ForegroundColor Red }
function Write-Step($m) { Write-Host "`n===== $m =====" -ForegroundColor Magenta }

function Test-CommandExists([string]$c) { return [bool](Get-Command $c -ErrorAction SilentlyContinue) }

# 从 Fleet yaml 里解析 image:(精确 tag,避免脚本写死过时)
function Get-FleetImage([string]$yamlRelPath) {
    $path = Join-Path $ProjectRoot $yamlRelPath
    if (-not (Test-Path $path)) { throw "找不到 Fleet 文件: $yamlRelPath" }
    $line = Select-String -Path $path -Pattern '^\s*image:\s*(\S+)' | Select-Object -First 1
    if (-not $line) { throw "未在 $yamlRelPath 解析到 image:" }
    return $line.Matches[0].Groups[1].Value
}

function Start-K8sEnvoyBridge {
    $bridgeScript = Join-Path $ScriptDir 'k8s_envoy_bridge.ps1'
    if (-not (Test-Path $bridgeScript)) { throw "缺少桥接脚本: $bridgeScript" }
    if ($BridgeForce) { & $bridgeScript -Force } else { & $bridgeScript }
    if ($LASTEXITCODE -ne 0) { throw "k8s Envoy 桥接启动失败" }
}

Write-Host ""
Write-Host "============================================" -ForegroundColor Magenta
Write-Host " Pandora 真 DS 闭环编排 (minikube + Agones)" -ForegroundColor Magenta
Write-Host "============================================" -ForegroundColor Magenta

# ── 0) 工具与集群就绪校验 ───────────────────────────────────────────────
Write-Step "[0/6] 校验 minikube / kubectl / Agones / 后端就绪"
foreach ($c in @('kubectl', 'minikube', 'docker')) {
    if (-not (Test-CommandExists $c)) { Write-Err "$c 未安装。先跑 start.ps1 -Mode k8s -Install。"; exit 1 }
}
minikube status *> $null
if ($LASTEXITCODE -ne 0) { Write-Err "minikube 未在运行。先跑:pwsh tools/scripts/start.ps1 -Mode k8s"; exit 1 }
Write-Ok "minikube 运行中"

kubectl get ns agones-system *> $null
if ($LASTEXITCODE -ne 0) { Write-Err "未检测到 Agones(agones-system)。先跑:pwsh tools/scripts/start.ps1 -Mode k8s"; exit 1 }
Write-Ok "Agones 已安装"

# Fleet 在不在(start.ps1 -Mode k8s 已 apply)
kubectl get fleet pandora-battle -n $FleetNamespace *> $null
$battleFleetOk = ($LASTEXITCODE -eq 0)
kubectl get fleet pandora-hub -n $FleetNamespace *> $null
$hubFleetOk = ($LASTEXITCODE -eq 0)
if (-not $battleFleetOk -or -not $hubFleetOk) {
    Write-Warn "Fleet 不全(battle=$battleFleetOk hub=$hubFleetOk),尝试 apply..."
    kubectl apply -f (Join-Path $AgonesDir '10-rbac-allocator.yaml')
    kubectl apply -f (Join-Path $AgonesDir '20-fleet-battle.yaml')
    kubectl apply -f (Join-Path $AgonesDir '30-fleet-hub.yaml')
}

# allocator 部署在不在 + 是否 agones 模式(读 configmap)
kubectl get deploy ds-allocator hub-allocator -n $K8sNamespace *> $null
if ($LASTEXITCODE -ne 0) {
    Write-Warn "ds-allocator / hub-allocator 未部署。先跑:pwsh tools/scripts/start.ps1 -Mode k8s"
}

# ── 1) load 两个真 UE Linux DS 镜像(精确 tag) ──────────────────────────
$battleImg = Get-FleetImage 'deploy/k8s/agones/20-fleet-battle.yaml'
$hubImg    = Get-FleetImage 'deploy/k8s/agones/30-fleet-hub.yaml'
Write-Step "[1/6] load 真 DS 镜像进 minikube"
Write-Info "battle: $battleImg"
Write-Info "hub:    $hubImg"

if ($SkipImageLoad) {
    Write-Warn "已传 -SkipImageLoad,跳过 image load"
} else {
    foreach ($img in @($battleImg, $hubImg)) {
        # 校验宿主 docker 里有该镜像(否则 minikube image load 会失败)
        docker image inspect $img *> $null
        if ($LASTEXITCODE -ne 0) {
            Write-Err "宿主 docker 没有镜像 $img"
            Write-Warn "  该镜像由 UE 侧 Linux DS 包构建(deploy/ds/build-image.sh)。"
            Write-Warn "  构建后重跑;或若 tag 不同,改 Fleet yaml 的 image: 再重试。"
            exit 1
        }
        Write-Info "  minikube image load $img ..."
        minikube image load $img
        if ($LASTEXITCODE -ne 0) { Write-Err "load 失败: $img"; exit 1 }
    }
    Write-Ok "两个 DS 镜像已 load"
}

# ── 2) 起宿主 Envoy 桥接 ────────────────────────────────────────────────
Write-Step "[2/6] 起宿主 Envoy 桥接(k8s Service -> host port-forward -> Envoy)"
Start-K8sEnvoyBridge

# ── 3) 等 Fleet Ready ──────────────────────────────────────────────────
Write-Step "[3/6] 等 Fleet Ready(超时 ${TimeoutSec}s)"
function Wait-FleetReady([string]$fleet, [int]$timeoutSec) {
    $deadline = (Get-Date).AddSeconds($timeoutSec)
    while ((Get-Date) -lt $deadline) {
        $ready = (kubectl get fleet $fleet -n $FleetNamespace -o jsonpath='{.status.readyReplicas}' 2>$null)
        if ([string]::IsNullOrWhiteSpace($ready)) { $ready = '0' }
        if ([int]$ready -ge 1) { Write-Ok "$fleet Ready=$ready"; return $true }
        Write-Host "  $fleet readyReplicas=$ready ..." -ForegroundColor DarkGray
        Start-Sleep -Seconds 5
    }
    Write-Err "$fleet 在 ${timeoutSec}s 内未就绪"
    kubectl get gameservers -n $FleetNamespace -L agones.dev/fleet 2>$null
    Write-Warn "排查:kubectl describe fleet $fleet -n $FleetNamespace;kubectl logs <gs-pod> -n $FleetNamespace -c $fleet-ds"
    return $false
}
$bOk = Wait-FleetReady 'pandora-battle' $TimeoutSec
$hOk = Wait-FleetReady 'pandora-hub' $TimeoutSec
if (-not $bOk -or -not $hOk) {
    Write-Err "Fleet 未全就绪,中止(DS 镜像未进 Ready 多半是 Agones SDK 未调通,看上面排查指引)。"
    exit 1
}

# ── 4) UDP 回程中继(docker driver 必需) ────────────────────────────────
Write-Step "[4/6] UDP 回程中继"
if ($NoRelay) {
    Write-Warn "已传 -NoRelay,未自动起中继。docker driver 下请另开终端跑:pwsh tools/scripts/udp_relay.ps1"
} else {
    Write-Info "后台拉起 UDP 中继(转发 127.0.0.1:<port> -> minikube 节点)..."
    $relay = Start-Process pwsh -PassThru -ArgumentList @(
        '-NoProfile', '-File', (Join-Path $ScriptDir 'udp_relay.ps1')
    )
    Start-Sleep -Seconds 2
    if ($relay.HasExited) {
        Write-Warn "中继进程已退出(可能 minikube ip 解析失败)。请手动跑 udp_relay.ps1 看报错。"
    } else {
        Write-Ok "UDP 中继已后台启动(PID=$($relay.Id))。停止:Stop-Process -Id $($relay.Id)"
    }
}

# ── 5) 现状打印 ────────────────────────────────────────────────────────
Write-Step "[5/6] 当前 Fleet / GameServer"
kubectl get fleet -n $FleetNamespace
kubectl get gameservers -n $FleetNamespace -L agones.dev/fleet,pandora.dev/region

# ── 6) 端到端验收清单 ──────────────────────────────────────────────────
Write-Step "[6/6] 真 DS 闭环验收(用真 UE 客户端)"
Write-Host @"
  后端入口(Envoy):客户端面 127.0.0.1:8443 / DS 面 127.0.0.1:8444
  确认客户端 DefaultGame.ini 后端指向本机 Envoy,然后:

  [ ] 登录 -> AssignHub 返回真 hub-ds 地址 -> ClientTravel 进大厅地图(MainCity)
  [ ] 匹配 ConfirmMatch -> matchmaker 调 AllocateBattle -> Agones 分配真 battle-ds -> 进战斗地图(MobaLevel)
  [ ] 打完 -> DS 上报 ReportResult 结算 -> ClientTravel 回大厅 hub-ds
  [ ] allocator 日志:allocator_mode=agones / fleet_mode=agones(不是 mock)

  实时观察:
    kubectl get gameservers -n $FleetNamespace -w
    kubectl logs deploy/ds-allocator  -n $K8sNamespace -f
    kubectl logs deploy/hub-allocator -n $K8sNamespace -f
"@ -ForegroundColor Gray

Write-Host ""
Write-Ok "真 DS 闭环环境就绪。按上面清单用真 UE 客户端验证。"

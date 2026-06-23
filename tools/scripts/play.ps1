<#
.SYNOPSIS
  Pandora 后端「策划一键启动」(只要装 Docker,双击即用)。

.DESCRIPTION
  面向策划的极简入口:不需要装 Go、不需要会编译,机器上只要有 Docker Desktop。
  做的事:
    1) 检查 Docker —— 没装就引导安装(能 winget 就自动装),没在跑就帮忙把 Docker Desktop 拉起来并等待就绪。
    2) Docker 就绪后,把整套后端跑起来(基础设施 + 15 个 go 服务全在容器里)。
       首次会在容器内编译镜像(稍慢),之后复用缓存秒起。
  本脚本只是 docker 模式(tools/scripts/start.ps1 -Mode docker)的「策划友好包装」,
  真正的构建/启动仍复用那条已验证的链路,不重复造轮子。

.EXAMPLE
  双击 仓库根目录\策划一键启动.cmd          # 启动整套后端(docker,不含战斗 DS)
  双击 仓库根目录\策划一键停止.cmd          # 停止
  双击 仓库根目录\策划一键启动-含战斗.cmd   # 本地战斗版(宿主 go 进程 + Windows DS)
  pwsh tools/scripts/play.ps1                # 启动(docker)
  pwsh tools/scripts/play.ps1 -Battle       # 本地战斗版
  pwsh tools/scripts/play.ps1 -Stop         # 停止
  pwsh tools/scripts/play.ps1 -Status       # 看状态
#>
[CmdletBinding()]
param(
    [switch]$Stop,     # 停止整套后端
    [switch]$Status,   # 只看状态,不启动
    [switch]$Battle    # 本地战斗模式:宿主 go 进程 + Windows DS(进 hub→匹配→battle 战斗)
)

$ErrorActionPreference = 'Stop'
$ScriptDir   = $PSScriptRoot
$ProjectRoot = (Resolve-Path "$ScriptDir/../..").Path
$StartPs1    = Join-Path $ScriptDir 'start.ps1'
$DsAllocConf = Join-Path $ProjectRoot 'services/battle/ds_allocator/etc/ds_allocator-dev.yaml'

# ===== 输出辅助 =====
function Write-Info($m) { Write-Host "[INFO] $m" -ForegroundColor Cyan }
function Write-Ok($m)   { Write-Host "[ OK ] $m" -ForegroundColor Green }
function Write-Warn($m) { Write-Host "[WARN] $m" -ForegroundColor Yellow }
function Write-Err($m)  { Write-Host "[ERR ] $m" -ForegroundColor Red }
function Write-Step($m) { Write-Host "`n===== $m =====" -ForegroundColor Magenta }

function Test-CommandExists([string]$cmd) {
    return [bool](Get-Command $cmd -ErrorAction SilentlyContinue)
}

function Test-DockerRunning {
    if (-not (Test-CommandExists 'docker')) { return $false }
    docker info *> $null
    return ($LASTEXITCODE -eq 0)
}

# 尝试找到 Docker Desktop 可执行文件
function Get-DockerDesktopExe {
    $candidates = @(
        (Join-Path $Env:ProgramFiles 'Docker\Docker\Docker Desktop.exe'),
        (Join-Path ${Env:ProgramFiles(x86)} 'Docker\Docker\Docker Desktop.exe')
    )
    foreach ($p in $candidates) {
        if ($p -and (Test-Path $p)) { return $p }
    }
    return $null
}

# 确保 Docker 命令存在
function Ensure-DockerInstalled {
    if (Test-CommandExists 'docker') {
        Write-Ok 'Docker 已安装'
        return $true
    }
    Write-Warn 'Docker 未安装'
    if (Test-CommandExists 'winget') {
        Write-Info '尝试用 winget 安装 Docker Desktop(可能要几分钟)...'
        winget install --id Docker.DockerDesktop --silent --accept-source-agreements --accept-package-agreements | Out-Null
    } else {
        Write-Err '未找到 winget,无法自动安装。'
    }
    Write-Host ''
    Write-Warn '请完成 Docker Desktop 安装(可能需要重启电脑),'
    Write-Warn '然后『启动 Docker Desktop』、等右下角鲸鱼图标变绿,再重新双击本脚本。'
    Write-Host '       手动下载:https://www.docker.com/products/docker-desktop/' -ForegroundColor Yellow
    return $false
}

# 确保 Docker daemon 在跑(没跑就尝试拉起 Docker Desktop 并等待就绪)
function Ensure-DockerRunning {
    if (Test-DockerRunning) {
        Write-Ok 'Docker 正在运行'
        return $true
    }
    Write-Warn 'Docker 已装但还没运行,尝试启动 Docker Desktop...'
    $exe = Get-DockerDesktopExe
    if ($exe) {
        Start-Process -FilePath $exe | Out-Null
    } else {
        Write-Warn '没找到 Docker Desktop 程序,请手动从开始菜单启动它。'
    }

    Write-Info 'Docker 首次启动较慢,正在等待就绪(最多约 3 分钟)...'
    $maxTries = 90          # 90 * 2s = 180s
    for ($i = 1; $i -le $maxTries; $i++) {
        if (Test-DockerRunning) {
            Write-Ok 'Docker 已就绪'
            return $true
        }
        Start-Sleep -Seconds 2
        if ($i % 10 -eq 0) { Write-Info "  仍在等待 Docker 启动... ($($i*2)s)" }
    }
    Write-Err 'Docker 还没就绪。请确认 Docker Desktop 已启动(右下角鲸鱼图标变绿)后重试。'
    return $false
}

# 从 ds_allocator-dev.yaml 读出本机 Windows DS 可执行文件路径(local_ds.executable_path)。
function Get-LocalDsExePath {
    if (-not (Test-Path $DsAllocConf)) { return $null }
    foreach ($line in (Get-Content $DsAllocConf)) {
        if ($line -match '^\s*executable_path:\s*"(.+?)"') {
            return $Matches[1].Trim()
        }
    }
    return $null
}

# 本地战斗模式预检:需要 Go(宿主进程)+ 打包好的 Windows DS。返回 $true=可继续。
function Test-BattlePrerequisites {
    $ok = $true

    if (-not (Test-CommandExists 'go')) {
        Write-Err 'Go 未安装。本地战斗版用「宿主 go 进程 + Windows DS」,需要装 Go(1.26.4+)。'
        Write-Host '       手动安装:https://go.dev/dl/    或在能联网时:winget install GoLang.Go' -ForegroundColor Yellow
        $ok = $false
    } else {
        Write-Ok 'Go 已就绪'
    }

    $exe = Get-LocalDsExePath
    if ([string]::IsNullOrEmpty($exe)) {
        Write-Warn '没在 ds_allocator-dev.yaml 找到 local_ds.executable_path。'
        Write-Warn '       请让 UE 同学打一个 Windows Server 包,再把 executable_path 指向 PandoraServer.exe。'
        $ok = $false
    } elseif (-not (Test-Path $exe)) {
        Write-Err "找不到 Windows DS 可执行文件:$exe"
        Write-Warn '       这是 UE 打包产物(PandoraServer.exe),不在本仓库。需要先让 UE 同学打一个 Windows Server 包,'
        Write-Warn '       并把 ds_allocator-dev.yaml 的 local_ds.executable_path 指到它。没有它,匹配成局后无法拉起战斗 DS。'
        $ok = $false
    } else {
        Write-Ok "Windows DS 已就绪:$exe"
    }

    return $ok
}

# ===== 主流程 =====
Write-Host ''
Write-Host '============================================' -ForegroundColor Magenta
Write-Host '  Pandora 后端  策划一键启动' -ForegroundColor Magenta
Write-Host '============================================' -ForegroundColor Magenta

if (-not (Test-Path $StartPs1)) {
    Write-Err "找不到 start.ps1:$StartPs1。请确认仓库完整(git pull)。"
    exit 1
}

# 看状态:不需要 daemon 也能看(看不到就提示)
if ($Status) {
    if ($Battle) {
        & $StartPs1 -Mode local -Status
    } else {
        & $StartPs1 -Mode docker -Status
    }
    exit $LASTEXITCODE
}

# 停止
if ($Stop) {
    if (-not (Test-CommandExists 'docker')) {
        Write-Warn 'Docker 未安装,无需停止。'
        exit 0
    }
    Write-Step '停止整套后端'
    if ($Battle) {
        & $StartPs1 -Mode local -Down
    } else {
        & $StartPs1 -Mode docker -Down
    }
    Write-Ok '已停止。'
    exit $LASTEXITCODE
}

# ===== 本地战斗版:宿主 go 进程 + Windows DS(进 hub → 匹配 → battle 战斗)=====
if ($Battle) {
    Write-Step '本地战斗版预检(需 Go + Windows DS)'
    Write-Info 'docker 版只跑 15 个后端服务,战斗 DS 是 Windows 程序、跑不进 Linux 容器,'
    Write-Info '所以「本地进战斗」走宿主 go 进程 + 本机 Windows DS。'
    if (-not (Test-BattlePrerequisites)) {
        Write-Err '本地战斗版前置条件不满足,见上方提示。'
        exit 1
    }

    Write-Step '检查 Docker(基础设施仍跑在 docker)'
    if (-not (Ensure-DockerInstalled)) { exit 1 }
    if (-not (Ensure-DockerRunning))   { exit 1 }

    # local 模式 + match 档:基础设施(docker) + 完整主链路 go 服务(宿主进程),
    # ds_allocator 走 mode=local,匹配成局后直接 exec 本机 Windows DS。
    & $StartPs1 -Mode local -Profile match
    $rc = $LASTEXITCODE
    Write-Host ''
    if ($rc -eq 0) {
        Write-Ok '本地战斗版后端已启动!'
        Write-Host '  - 客户端网关(Envoy): https://127.0.0.1:8443' -ForegroundColor Green
        Write-Host '  - 现在用 UE 客户端登录 → 进大厅 → 匹配,成局后会自动拉起本机 Windows DS 进战斗。' -ForegroundColor Green
        Write-Host '  - 停止:        pwsh tools/scripts/play.ps1 -Battle -Stop' -ForegroundColor DarkGray
    } else {
        Write-Err '启动过程中出错了,请把上面的红色 [ERR] 信息发给后端同学。'
    }
    exit $rc
}

# 启动:先把 Docker 准备好
Write-Step '检查 Docker'
if (-not (Ensure-DockerInstalled)) { exit 1 }
if (-not (Ensure-DockerRunning))   { exit 1 }

# 委托给已验证的 docker 模式:基础设施 + 15 个 go 服务全容器化
# (首次会在容器内编译镜像,稍慢;之后复用缓存。策划本机不需要装 Go。)
& $StartPs1 -Mode docker
$rc = $LASTEXITCODE

Write-Host ''
if ($rc -eq 0) {
    Write-Ok '后端已启动!'
    Write-Host '  - 客户端网关(Envoy): https://127.0.0.1:8443' -ForegroundColor Green
    Write-Host '  - 看运行状态:  双击 策划一键启动.cmd 旁边的 -Status,或 pwsh tools/scripts/play.ps1 -Status' -ForegroundColor DarkGray
    Write-Host '  - 停止:        双击 策划一键停止.cmd' -ForegroundColor DarkGray
} else {
    Write-Err '启动过程中出错了,请把上面的红色 [ERR] 信息发给后端同学。'
}
exit $rc

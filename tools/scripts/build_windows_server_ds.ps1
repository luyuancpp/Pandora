# Build Windows Dedicated Server (PandoraServer target) for local mode.
#
# local mode must run a staged/cooked WindowsServer package. Do not point
# allocator configs at the raw Pandora/Binaries/Win64/PandoraServer.exe build.
#
# Usage from backend repo root:
#   pwsh tools/scripts/build_windows_server_ds.ps1
# Optional:
#   -EngineDir "F:\UnrealEngine-5.8.0-release"
#   -Project "F:\work\Pandora-Client-SVN\Pandora\Pandora.uproject"
#   -Archive "F:\work\PandoraDSArchive\WindowsServerLocal"

[CmdletBinding()]
param(
    [string]$EngineDir = $(if ($env:UE_ENGINE_DIR) { $env:UE_ENGINE_DIR } else { "F:\UnrealEngine-5.8.0-release" }),
    [string]$Project = "F:\work\Pandora-Client-SVN\Pandora\Pandora.uproject",
    [string]$Archive = "F:\work\PandoraDSArchive\WindowsServerLocal",
    [int]$MaxParallelActions = 6
)

$ErrorActionPreference = "Stop"

function Resolve-FullPath([string]$path) {
    if ([string]::IsNullOrWhiteSpace($path)) { return $path }
    $full = [System.IO.Path]::GetFullPath($path)
    return $full.TrimEnd('\', '/')
}

function Find-StagedServerExe([string]$archiveDir) {
    $candidates = @(
        (Join-Path $archiveDir "WindowsServer\Pandora\Binaries\Win64\PandoraServer.exe"),
        (Join-Path $archiveDir "Win64Server\Pandora\Binaries\Win64\PandoraServer.exe"),
        (Join-Path $archiveDir "Pandora\Binaries\Win64\PandoraServer.exe")
    )

    foreach ($candidate in $candidates) {
        if (Test-Path $candidate) { return (Resolve-Path $candidate).Path }
    }

    $found = Get-ChildItem -LiteralPath $archiveDir -Recurse -Filter PandoraServer.exe -File -ErrorAction SilentlyContinue |
        Sort-Object FullName |
        Select-Object -First 1
    if ($found) { return $found.FullName }

    return $null
}

function Find-StagedRoot([string]$serverExe) {
    $dir = Split-Path -Parent $serverExe
    while ($dir) {
        $pandoraDir = Join-Path $dir "Pandora"
        $engineDir = Join-Path $dir "Engine"
        if ((Test-Path $pandoraDir) -and (Test-Path $engineDir)) {
            return $dir
        }
        $parent = Split-Path -Parent $dir
        if ($parent -eq $dir) { break }
        $dir = $parent
    }
    return $null
}

$EngineDir = Resolve-FullPath $EngineDir
$Project = Resolve-FullPath $Project
$Archive = Resolve-FullPath $Archive
$RunUAT = Join-Path $EngineDir "Engine\Build\BatchFiles\RunUAT.bat"

if (-not (Test-Path $RunUAT)) {
    throw "RunUAT.bat not found: $RunUAT"
}
if (-not (Test-Path $Project)) {
    throw "Pandora.uproject not found: $Project"
}

New-Item -ItemType Directory -Force -Path $Archive | Out-Null

Write-Host "Engine:  $EngineDir"
Write-Host "Project: $Project"
Write-Host "Archive: $Archive"
Write-Host "MaxParallelActions: $MaxParallelActions"

$uatArgs = @(
    "BuildCookRun",
    "-project=$Project",
    "-noP4",
    "-nodebuginfo",
    "-clientconfig=Development",
    "-serverconfig=Development",
    "-server",
    "-noclient",
    "-platform=Win64",
    "-serverplatform=Win64",
    "-cook",
    "-build",
    "-stage",
    "-pak",
    "-prereqs",
    "-archive",
    "-archivedirectory=$Archive",
    "-ddc=InstalledNoZenLocalFallback"
)

if ($MaxParallelActions -gt 0) {
    $uatArgs += "-ubtargs=-MaxParallelActions=$MaxParallelActions"
}

& $RunUAT @uatArgs
if ($LASTEXITCODE -ne 0) {
    throw "BuildCookRun failed with exit code $LASTEXITCODE"
}

$serverExe = Find-StagedServerExe $Archive
if (-not $serverExe) {
    throw "Build finished, but no staged PandoraServer.exe was found under: $Archive"
}

$stagedRoot = Find-StagedRoot $serverExe
if (-not $stagedRoot) {
    throw "Found PandoraServer.exe but could not locate staged root with Engine/ and Pandora/: $serverExe"
}

$pakDir = Join-Path $stagedRoot "Pandora\Content\Paks"
$pak = Get-ChildItem -LiteralPath $pakDir -Filter "*.pak" -File -ErrorAction SilentlyContinue |
    Select-Object -First 1
if (-not $pak) {
    throw "Staged server is missing cooked pak files: $pakDir"
}

Write-Host ""
Write-Host "Windows Server DS build is ready." -ForegroundColor Green
Write-Host "Executable:  $serverExe"
Write-Host "WorkingDir:  $stagedRoot"
Write-Host "PakDir:      $pakDir"
Write-Host ""
Write-Host "Allocator configs should use the Executable and WorkingDir above." -ForegroundColor Yellow

@echo off
rem ============================================================
rem  Pandora 后端  策划一键启动-含战斗(双击即用)
rem ------------------------------------------------------------
rem  本地完整战斗版:能进大厅、匹配、进 battle DS 打一局。
rem  与「策划一键启动.cmd」(docker 版)的区别:
rem    - docker 版:只跑 15 个后端服务,不含战斗 DS(战斗 DS 是 Windows
rem      程序,跑不进 Linux 容器)。
rem    - 本版:后端走宿主进程(需装 Go),匹配成局后自动拉起本机的
rem      Windows DS(PandoraServer.exe)进战斗。
rem
rem  前置条件(脚本会自动检查并清晰提示):
rem    1) 装了 Go(1.26.4+)和 Docker Desktop。
rem    2) 有一个 UE 打好的 Windows Server 包(PandoraServer.exe),
rem       并已在 ds_allocator-dev.yaml 的 local_ds.executable_path 指向它。
rem
rem  停止请双击:策划一键停止.cmd 不适用本版,请用命令:
rem    pwsh tools\scripts\play.ps1 -Battle -Stop
rem ============================================================
setlocal
cd /d "%~dp0"

where pwsh >nul 2>nul && (set "PS=pwsh") || (set "PS=powershell")

%PS% -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\scripts\play.ps1" -Battle
set "RC=%ERRORLEVEL%"

pause
exit /b %RC%

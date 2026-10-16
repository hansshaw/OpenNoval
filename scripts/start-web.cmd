@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0.."
set "WEB_PORT=17680"
if not "%~1"=="" set "WEB_PORT=%~1"
if not defined AINOVEL_GO (
  set "AINOVEL_GO=go"
  where go >nul 2>nul
  if errorlevel 1 set "AINOVEL_GO=%LOCALAPPDATA%\Programs\ainovel-go-1.25.5\go\bin\go.exe"
)
powershell.exe -NoProfile -Command "$port=0; if (-not [int]::TryParse($env:WEB_PORT, [ref]$port) -or $port -lt 1 -or $port -gt 65535) { exit 1 }"
if errorlevel 1 (
  echo 端口无效，请输入 1 到 65535 之间的整数。
  goto :failed
)
"%AINOVEL_GO%" version >nul 2>nul
if errorlevel 1 (
  echo 未找到可用的 Go，请安装 go.mod 要求的 Go 版本，或设置 AINOVEL_GO 为 go.exe 路径。
  goto :failed
)
if not defined GOPROXY set "GOPROXY=https://goproxy.cn,direct"
set "GOWORK=off"
echo 正在构建 Web 工作台...
"%AINOVEL_GO%" build -buildvcs=false -o output/local-test/ainovel-web-current.exe ./cmd/ainovel-cli
if errorlevel 1 (
  echo 构建失败，请检查 Go 环境或关闭正在运行的同名程序后重试。
  goto :failed
)
echo 正在启动小说 Web 工作台；监听成功后自动打开浏览器。
echo 当前终端保持运行；按 Ctrl+C 停止服务。
"output\local-test\ainovel-web-current.exe" --web --open-browser --listen 127.0.0.1:%WEB_PORT%
set "WEB_EXIT=%ERRORLEVEL%"
if not "%WEB_EXIT%"=="0" echo 启动失败，请检查上方错误；端口被占用时可换一个端口重试。
pause
exit /b %WEB_EXIT%

:failed
pause
exit /b 1

@echo off
chcp 65001 >nul
cd /d "%~dp0"

REM ============ 截图识别配置（可选）============
REM 不配置也能用，只是「上传截图自动识别」不可用，可手动录入。
REM 支持任意 OpenAI 兼容的视觉模型端点（OpenAI / DeepSeek / 通义千问 / Moonshot / Ollama 等）。
REM 去掉下面几行开头的 REM 并填入你自己的密钥即可启用：
REM set OCR_API_KEY=sk-你的密钥
REM set OCR_API_BASE=https://api.openai.com/v1
REM set OCR_MODEL=gpt-4o-mini

set PORT=8080

if not exist "xianyu-stat.exe" (
  echo 未找到 xianyu-stat.exe，请先运行 build.sh 完成构建。
  pause
  exit /b
)

echo 正在启动「闲鱼售卖统计」服务 ...
start "闲鱼售卖统计 - 服务窗口（关闭即停止）" cmd /c "xianyu-stat.exe & echo. & pause"
timeout /t 2 >nul
start "" http://localhost:%PORT%

echo.
echo 服务已启动，浏览器已打开 http://localhost:%PORT%
echo 数据保存在同目录的 xianyu.db 中，请勿删除。
echo 关闭刚弹出的黑色服务窗口即可停止服务。
echo.
pause

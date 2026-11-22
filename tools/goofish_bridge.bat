@echo off
chcp 65001 >nul
title 闲鱼浏览器保活桥
echo.
echo   闲鱼浏览器保活桥（真实浏览器）
echo   首次运行：选店铺，手机扫码登录
echo   之后：窗口可最小化，不要关闭；登录失效就在窗口里重新扫码
echo   换服务器：改同目录 config.json，或命令行加 --server http://地址:端口
echo.
node "%~dp0goofish_bind.js" --watch %*
echo.
echo   保活桥已停止。服务器仍可拉单，但登录失效后不会自动换新 Cookie。
pause

@echo off
chcp 65001 >nul
title 闲鱼一键绑定
echo.
echo   闲鱼一键绑定（真浏览器扫码）
echo   选店铺：直接双击按提示选；或命令行加 --shop 店铺ID 跳过选择
echo   换服务器：改同目录 config.json，或命令行加 --server http://地址:端口
echo.
node "%~dp0goofish_bind.js" %*
echo.
pause

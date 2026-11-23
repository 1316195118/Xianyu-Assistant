# 鱼助手用户数据处理盘点

审计日期：2026-11-23

本清单按当前代码中的实际功能整理，用于隐私政策、部署检查和后续变更复核。具体源码入口见各节标注。

## 1. 账户与会话

- 管理密码、店铺密码、只读密码：请求体提交到自建服务，服务端用 scrypt 加盐哈希保存，不保存明文。入口：`server/auth.go`、`web/src/components/LoginView.vue`、`web/src/views/Settings.vue`。
- 登录会话：使用 `xy_session` HttpOnly Cookie，SameSite=Lax，HTTPS 下 Secure，最长 7 天。入口：`server/auth.go`。
- 店铺选择：当前店铺 ID 保存到浏览器 localStorage；只要存在店铺密码，本次会话是否选店另存到 sessionStorage。入口：`web/src/shopId.js`、`web/src/App.vue`。

## 2. 经营数据

- 交易记录：商品、买卖双方昵称、订单号、成交价、运费、软件服务费、无忧卖费用、成本、时间、备注、来源、归属人、数量、附件名。入口：`server/db.go`、`server/handlers.go`、`web/src/components/RecordModal.vue`。
- 联单：标题、备注、成员订单、外部成交、完结状态和成本分摊结果。入口：`server/groups.go`、`web/src/views/Groups.vue`。
- 寄卖与代采：归属人、商品、金额、垫付、结算流水和备注。入口：`server/extras.go`、`web/src/views/Consign.vue`。
- 月度目标、运费标签、识别反馈和撤销快照：保存在自建服务 SQLite。入口：`server/settings.go`、`server/extras.go`、`server/undo.go`。

所有经营数据默认保存在部署者自己的 SQLite 数据库和附件目录中，不上报到项目维护者。

## 3. 截图和 OCR

- 用户上传订单截图：原图存于部署服务器的 `attachments/`，系统生成缩略图。
- 默认本地 OCR：Tesseract WASM 在浏览器内运行，识别时不上传图片。
- 可选视觉 OCR：图片先提交给自建服务 `/api/recognize`，由后端代理到部署者配置的 OpenAI 兼容视觉接口。
- OCR API Key：可保存在自建 SQLite，或由 `OCR_API_KEY` 环境变量提供；设置接口只返回脱敏状态。入口：`server/ocr.go`、`web/src/ocr.js`、`web/src/views/Settings.vue`。
- OCR 反馈：准确/需修正状态、关联记录 ID、备注和时间。入口：`server/extras.go`。

## 4. 闲鱼登录与同步

- 浏览器扩展读取 goofish.com、taobao.com、tmall.com 的相关 Cookie，发送到用户配置的鱼助手服务器。
- 服务端保存闲鱼 Cookie、淘宝 SSO Cookie、昵称、绑定店铺和同步状态，用于订单和费用同步。
- 扫码登录保留为实验功能；正式入口为扩展内打开闲鱼官方登录页并捕获登录态。
- 扩展配置保存在 `chrome.storage.local`，包括服务器地址、店铺 ID、自动同步和登录刷新状态。

入口：`tools/goofish_extension/`、`server/goofish*.go`。

## 5. 浏览器本地状态

- localStorage：主题、减少动效、当前店铺、表格排序和列显示、安装提示、更新提示、录单草稿。
- sessionStorage：客户端实时同步标识、选店会话标记、预加载错误限流和更新提示限流。
- Service Worker Cache：页面壳和带哈希的前端资源，用于离线启动；不缓存 `/api/*`。

## 6. 备份与导出

- 自动备份：部署者可配置周期和保留份数，保存在数据库同目录的 `backups/` 或 `BACKUP_DIR`。
- 手动导出：JSON、CSV、Excel 只导出当前店铺；导出文件不包含访问密码和 OCR API Key。
- 撤销快照：部分删除、覆盖导入和备份删除操作会生成短时撤销数据。

## 7. 日志与网络请求

- 应用访问日志记录请求方法、路径、状态码和耗时；未主动记录请求体、Cookie、密码或 IP。
- 部署者使用的反向代理、CDN、隧道或托管平台可能另行记录 IP、User-Agent 和访问时间。
- 更新检查访问公开 GitHub Release，不需要 Token；可选代理可能看到连接元数据。
- 闲鱼同步会访问闲鱼/淘宝接口；视觉 OCR 会访问用户指定的模型接口。

## 8. 未发现的项目

- 未接入 Google Analytics、GA4、Plausible、Umami、Sentry、广告像素、热力图或会话回放脚本。
- 未发现硬编码的真实第三方 API Key、GitHub Token 或私钥。
- 未使用外部 Web Font；使用系统字体。
- 未使用精确定位、麦克风、摄像头或支付卡表单。

## 9. 上线配置提醒

1. 使用 HTTPS，并由实际部署运营者配置 HSTS 和 HTTP 到 HTTPS 跳转。
2. 设置强管理密码；公开网络部署必须开启访问密码。
3. 限制 `xianyu.db`、`attachments/`、`backups/` 的文件权限和备份访问权限。
4. 不要把 `.env`、数据库、Cookie、API Key 或订单截图提交到 Git。
5. 若部署者自行加入统计或营销 SDK，必须更新本盘点、隐私政策和相应同意机制。

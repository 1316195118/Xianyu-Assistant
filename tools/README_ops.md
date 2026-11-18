# 运维脚本说明（咸鱼记录）

本目录脚本默认**不执行任何生产变更**：`rollback_pve.sh` 默认 dry-run，`sync_backup_remote.sh` 未设置目标变量时只打印用法。
脚本路径均由项目根 / `$HOME` 推导，不写死用户名目录；PVE 凭据从历史审计记录提取到环境变量，绝不回显。

## 脚本一览与用法

| 脚本 | 用途 | 示例 |
| --- | --- | --- |
| `rollback_pve.sh` | 一键回滚 CT100（默认选最新备份） | `./tools/rollback_pve.sh --list` / `./tools/rollback_pve.sh`（预览）/ `./tools/rollback_pve.sh -y`（真执行） |
| `sync_backup_remote.sh` | 容器内 backups 同步到远端 | `REMOTE_BACKUP_TARGET=backup@nas:/srv/xy ./tools/sync_backup_remote.sh --dry-run` |
| `verify_backup.py` | 线上 items 数 vs 最新 db 备份对账 | `python tools/verify_backup.py --host http://10.10.10.251:18083 --backup-dir ./backups` |
| `bench_api.py` | 分页/索引压测 | `python tools/bench_api.py -n 20 --q 手机 --page-sizes 20,50,100` |
| `web/public/sw.js` | Service Worker（离线缓存 / 版本检查） | 页面注册 `navigator.serviceWorker.register('/sw.js')` |

环境变量：`XY_HOST`、`XY_BACKUP_DIR`、`XY_COOKIE`（接口开密码时传 `xy_session=...`）、`XY_TIMEOUT`、`REMOTE_BACKUP_TARGET`、`CT_BACKUP_DIR`。
退出码约定：`verify_backup.py` 0=一致 / 1=不一致 / 2=出错。

> **Service Worker**：`web/public/sw.js` 由 `tools/sync_webroot.py` 一并合并进 `server/webroot`，
> 前端在 `web/src/main.js` 里注册 `/sw.js`，因此每次构建都会带上，不需要额外部署步骤。

---

## 需要使用者自行提供的外部条件

### 1. HTTPS / 反向代理
- 后端 `PORT=18083` 是纯 HTTP，生产公网暴露请前置反向代理（Nginx / Caddy / Cloudflare Tunnel）终止 TLS。
- 自行准备：域名、TLS 证书（或 ACME 自动签发）、反代配置。
- 反代需透传 `Cookie`（`xy_session`），并放行 `GET /api/export`、`/api/records`、`/api/backups`、`/api/health` 及静态资源。
- 若用自签证书/内网 CA，客户端需先信任根证书；`verify_backup.py` / `bench_api.py` 的 `--host` 改成 `https://域名`。

### 2. 异地同步凭据（`sync_backup_remote.sh`）
- 提供 `REMOTE_BACKUP_TARGET`（如 `user@host:/path`），以及**远端主机的 SSH 登录凭据**：
  在 PVE 宿主上为运行 `pve_ssh.py` 的账号配置到远端的免密 SSH key（`~/.ssh/id_*` + `known_hosts`），
  或提供 `rsync`/`scp` 可用的认证方式。
- 该脚本经 `pve_ssh.py run` 在 **PVE 宿主** 上执行 `rsync`/`scp`，故凭据需存在于 PVE 宿主，而非本机。
- 未设置 `REMOTE_BACKUP_TARGET` 时脚本只打印用法并退出 0；建议先用 `--dry-run` 预览再实跑。
- 注意 `--delete-after` 会删除远端多出的文件，请先确认目标目录专用于本备份、且已做 `--dry-run`。

### 3. 真机复测清单（上线前/回滚后）
- [ ] 浏览器硬刷新 + 无痕窗口，确认加载的是最新 `xy-build`（`view-source` 看 `<meta name="xy-build">`）。
- [ ] 手机端（iOS Safari / Android Chrome）分别验证：首页、分页、带 `q` 搜索、导出、登录态、深色模式。
- [ ] 断网/弱网下验证 SW 离线回退；确认 `UPDATE_AVAILABLE` 能触发提示并可刷新到新版。
- [ ] `./tools/verify_backup.py` 对账为 `consistent: true`。
- [ ] `./tools/bench_api.py` 在真机网络下 p95 可接受、失败数为 0。
- [ ] 回滚演练：在**非生产时段**用 `rollback_pve.sh`（先不带 `-y` 看计划，再 `-y`）验证能恢复并 `/` 返回 200。
- [ ] `/api/health`（若后端已提供）返回正常；`/api/export`、`/api/backups` 鉴权/下载可用。

### 4. 公网安全核查清单
- [ ] 强密码已开启（`/api/*` 受 `authWrap` 保护），弱口令禁用；`xy_session` 仅经 HTTPS 传输。
- [ ] 管理端口 18083 不直接对公网开放，仅反代暴露；PVE / SSH（22）限制来源 IP。
- [ ] 反代开启 HSTS、限制请求体大小、隐藏后端版本头；对 `/api/backups`、`/api/export` 做访问审计。
- [ ] 异地同步目标目录权限最小化，备份文件含业务数据，静态加密/访问控制到位。
- [ ] 定期轮换：访问密码、PVE 凭据、远端 SSH key；审计 `rollback`/`sync` 的执行记录。
- [ ] 确认 `rollback_pve.sh` 的 dry-run 默认、`-y` 门禁在运维流程中被遵守，避免误操作。

## 闲鱼数据抓取 / 对账脚本（2026-11）

产品里已经有「设置 → 闲鱼同步」，正常使用不需要这些脚本。它们保留下来是为了排查问题与重新核对口径。

| 脚本 | 用途 |
| --- | --- |
| `mtop_client.py` | 最小 mtop 客户端（`sign = md5(token&t&appKey&data)`），直连 `h5api.m.goofish.com`，不依赖浏览器 |
| `fetch_income.py` | 拉收入账单明细（`mtop.idle.finance.billdetail.income.query`，`billCode=goods_income`），`incomeActualOrderAmount` 是真实成交额 |
| `fetch_fees.py` | 拉支出账单明细（`mtop.idle.finance.billdetail.expense.query`，`billCode=common_settle`），按 `bizCode` 取无忧卖 / 软件服务费 / 基础软件服务费 |
| `normalize_fees.py` | 按 `bizId` 去重并按订单聚合（账单接口分页会重复吐同一条） |
| `match_fees.py` | 把卖单与费用明细按订单号对账，输出差异 |
| `plan_sell.py` | 生成「成交价 / 服务费 / 到手价」修正计划 |
| `apply_sell_fix.py` | 把计划通过 `PUT /api/records/{id}` 写回，不带 `--apply` 时只预览 |
| `check_goofish_ui.py` | 用 CDP 在 390 / 1280 两个宽度下跑一遍「设置 → 闲鱼同步」并截图 |

口径（2026-11 与用户确认）：
- 成交价 = 账单 `incomeActualOrderAmount`（买家实付，不含运费），**不是**商品标价 `incomeAuctionPrice`
- 软件服务费 = 1% 计费基数，基础软件服务费 = 0.6%，无忧卖 = 命中类目时再 1%
- 到手价 = 成交价 − 软件服务费 − 基础软件服务费 − 无忧卖 − 卖运费（与明细页展示一致）

已知限制（2026-11 实测）：
- 账单接口单次业务日期跨度不能超过 31 天（`INVALID_PARAM`），必须按月查
- 当月账单要到次月 3 号后才生成（`BILL_QUERY_DELAY_ERROR`）
- **卖单列表固定只回 40 单**（`totalCount=40`，翻页/`pageSize`/时间筛选全部无效），约等于「近 3 个月」；
  更早的订单只能按账单期翻「收入明细」补建（产品里的「账单基准回溯」就是干这个）。
- 「我买到的」翻页翻到重复页会自动停（脚本按单号去重），不会有固定页数上限。
- 扫码登录被阿里风控拦截（`umidToken` 缺失时二维码立刻 EXPIRED），所以只能粘 Cookie

产品实现补充（与脚本阶段的口径差异）：
- 卖单状态：**已发货 / 待发货也导入**（钱已付），只有退款 / 交易关闭 / 取消 / 待付款才跳过
- 支付宝账单导入已恢复：手续费按订单号精确匹配，快递费优先按闲鱼 `consignTime` 对时（实测差 2~5 秒），
  没有发货时间才退回按成交时间推测；已完结记录默认不动，勾「覆盖已完结」才写。

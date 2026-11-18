# 第三方组件与版权声明

本程序为**个人本地记账工具**，界面与业务逻辑为原创实现。程序**不包含**闲鱼（Xianyu / 阿里巴巴集团）的任何商标、图标、截图、界面素材或官方数据；用户上传的订单截图仅在本机内存中处理，不随程序分发、不上传第三方（除非用户在「进阶设置」中主动配置并启用视觉大模型 API，此时图片仅发送给用户自己指定的接口）。

「闲鱼」为阿里巴巴集团旗下商标，此处仅作描述性用途，用于说明本工具所处理的数据类型，不表示任何隶属或授权关系。

---

## 一、随二进制分发的开源组件

以下组件的全部或部分代码 / 构建产物被编译或嵌入 `xianyu-stat.exe`。各组件的**完整许可证原文**已随程序嵌入，可在程序内「设置 → 开源许可」查看，或访问 `/licenses/<文件名>`；机器可读索引见 `/licenses/index.json`。

### 本地 OCR 能力（默认引擎，完全离线）

| 组件 | 版本 | 许可证 | 用途 | 来源 |
|---|---|---|---|---|
| tesseract.js | 5.1.1 | Apache-2.0 | 浏览器端 OCR 调度 | github.com/naptha/tesseract.js |
| tesseract.js-core | 5.1.1 | Apache-2.0 | Tesseract WASM 内核（SIMD+LSTM） | github.com/naptha/tesseract.js-core |
| tessdata_fast / chi_sim.traineddata.gz | 4.0.0_fast | Apache-2.0 | 简体中文识别语言模型 | github.com/tesseract-ocr/tessdata_fast |
| Tesseract OCR | — | Apache-2.0 | 上述 WASM 内核的上游引擎 | github.com/tesseract-ocr/tesseract |

> 中文语言模型取自 `tessdata_fast`（Apache-2.0），以 `chi_sim.traineddata.gz` 形式原样分发，未作修改。

### 前端

| 组件 | 版本 | 许可证 | 来源 |
|---|---|---|---|
| Vue | 3.5.45 | MIT | github.com/vuejs/core |
| Vite | 6.4.6 | MIT | github.com/vitejs/vite |
| @vitejs/plugin-vue | 5.2.4 | MIT | github.com/vitejs/vite-plugin-vue |

### 后端（Go 模块）

`modernc.org/sqlite` 及其传递依赖，绝大多数为 **BSD-3-Clause**，其余为 MIT / Apache-2.0 / MPL-2.0：

- **BSD-3-Clause**：modernc.org/{sqlite, libc, cc/v4, ccgo/v4, fileutil, mathutil, memory, opt, sortutil, strutil, token, goabi0, gc/v2, gc/v3}、golang.org/x/{mod, sync, sys, tools}、github.com/google/uuid、github.com/remyoudompheng/bigfft
- **MIT**：github.com/dustin/go-humanize、github.com/mattn/go-isatty、github.com/ncruces/go-strftime
- **Apache-2.0**：github.com/google/pprof
- **MPL-2.0**：github.com/hashicorp/golang-lru/v2

每个模块的准确版本与对应许可证原文文件，见程序内「设置 → 开源许可」列表。

---

## 二、合规说明

1. Apache-2.0 组件：随程序附带 `LICENSE-2.0` 全文；未修改其源码，故无额外变更声明。
2. MIT / BSD-3-Clause 组件：随程序附带各组件原始版权声明与许可条款全文，满足「二进制分发需在文档或随附材料中复现版权声明」的要求。
3. MPL-2.0 组件（golang-lru/v2）：以未修改的独立模块形式链接入二进制，随附 `MPL-2.0` 全文；其源文件仍可从上游仓库获取。
4. 若后续更换组件版本，请重新运行 `tools/collect_licenses.py`（见下）刷新 `/licenses/` 内容后再构建。

## 三、生成方式

`static/licenses/` 下的许可证原文由脚本自动从 `go env GOMODCACHE` 与各 npm 包的 `LICENSE*` 收集，并抓取 Apache-2.0 / MPL-2.0 官方全文，同时生成 `index.json`。构建时由 `tools/sync_webroot.py` 合并进 `server/webroot/licenses/`，再由 Go `//go:embed webroot` 打进单文件二进制，因此**单独分发 exe 时许可声明依旧可查**（也可在程序内「设置 → 开源许可与版权声明」查看）。

---

## 四、数据来源与隐私

- 所有订单数据保存在本机 `xianyu.db`，不上传。
- 本地 OCR 引擎全程在浏览器内用 WASM 运行，**不联网、不上传图片**。
- 「视觉大模型」为可选的进阶引擎，默认关闭；启用后图片仅发送至用户自行配置的 API 端点，请在启用前自行确认该服务商的数据处理条款。

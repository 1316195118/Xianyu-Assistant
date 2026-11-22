/**
 * 闲鱼一键绑定（通用版）：真浏览器扫码 → 自动收 Cookie 绑定到你自己的记账服务器。
 *
 * 给「其他玩家 / 其他部署」用，无需改代码。三个问题一次解决：
 *   1. 服务器地址不一样      → 命令行 --server 或同目录 config.json，首次运行会引导填写
 *   2. 要选绑到哪个店铺      → 启动时自动拉取店铺列表让你选（或 --shop 直接指定）
 *   3. 多店 Cookie 串号      → 每个店铺用独立浏览器 profile（profiles/shop-<id>）
 *
 * 用法（双击 goofish_bind.bat 即可，命令行参数为可选）：
 *   node goofish_bind.js [--server http://你的服务器:端口] [--shop 店铺ID]
 *   node goofish_bind.js --watch [--server ...] [--shop ...]
 *
 * --watch 会保留专用浏览器并进入保活桥模式：登录态一变化就自动上报，
 * 浏览器可最小化，但不要关闭；关闭后服务器仍可正常拉单，只是不能自动换新 Cookie。
 *
 * 依赖：Node.js 18+（内置 fetch / WebSocket）、本机 Microsoft Edge 或 Google Chrome。
 * Cookie 只在你电脑和你的服务器之间传递，不回显、不上传第三方。
 */
const { spawn } = require('node:child_process')
const { createHash } = require('node:crypto')
const fs = require('node:fs')
const path = require('node:path')
const readline = require('node:readline')

const LOGIN = 'https://www.goofish.com/login'
const CONFIG = path.join(__dirname, 'config.json')
const PROFILES = path.join(__dirname, 'profiles')
const DEFAULT_SERVER = 'http://10.10.10.251:18083'
const WATCH_TICK = 15_000
const STATUS_TICK = 60_000
const FAILED_UPLOAD_RETRY = 10 * 60_000
const PAGE_REFRESH = 2 * 60 * 60_000

// 只监听登录/设备身份相关的 Cookie。_m_h5_tk 等签名令牌会频繁轮换，
// 如果把它算进指纹，会把同一个登录态每十几秒重复上报一次。
const AUTH_COOKIE_NAMES = new Set([
  'cookie2', 'sgcookie', 'unb', 'cna', 't', '_tb_token_',
  'havana_lgc_exp', 'cookie3_bak_exp', 'x5sec'
])

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const ask = (q) => new Promise((res) => {
  const rl = readline.createInterface({ input: process.stdin, output: process.stdout })
  rl.question(q, (a) => { rl.close(); res(a.trim()) })
})

// ---------- 浏览器路径自动探测（Edge 优先，其次 Chrome）----------
const CANDIDATES = [
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
  process.env.LOCALAPPDATA ? path.join(process.env.LOCALAPPDATA, 'Microsoft/Edge/Application/msedge.exe') : '',
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe',
  process.env.LOCALAPPDATA ? path.join(process.env.LOCALAPPDATA, 'Google/Chrome/Application/chrome.exe') : '',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome', '/usr/bin/chromium'
].filter(Boolean)

function findBrowser() { return CANDIDATES.find((p) => fs.existsSync(p)) || '' }

function cookiePairs(raw) {
  const out = {}
  for (const part of String(raw || '').split(';')) {
    const i = part.indexOf('=')
    if (i <= 0) continue
    const name = part.slice(0, i).trim()
    if (AUTH_COOKIE_NAMES.has(name)) out[name] = part.slice(i + 1).trim()
  }
  return out
}

// 指纹只留在内存中，用于判断登录态是否变化；不记录、不上报 Cookie 原文。
function authFingerprint(goofish, taobao) {
  const pairs = [
    ...Object.entries(cookiePairs(goofish)).map(([k, v]) => `g:${k}=${v}`).sort(),
    ...Object.entries(cookiePairs(taobao)).map(([k, v]) => `t:${k}=${v}`).sort()
  ]
  return createHash('sha256').update(pairs.join('\n')).digest('hex')
}

function statusNeedsBinding(status) {
  return !status || !status.logged_in || !!status.expired || !!status.sync_error
}

// 专用 profile 可能恢复上次的 ChatGPT 等其他标签页。必须明确选闲鱼登录页，
// 否则会把“切换扫码”和会话轮询发到无关页面，用户看起来就是扫码没反应。
function pickLoginPage(pages) {
  const list = Array.isArray(pages) ? pages : []
  return list.find((x) => x && x.type === 'page' && /^https:\/\/(?:www\.)?goofish\.com\/login(?:[/?#]|$)/.test(x.url || ''))
    || list.find((x) => x && x.type === 'page' && /goofish\.com/i.test(x.url || ''))
    || null
}

// ---------- 配置读写 ----------
function loadConfig() {
  try { return JSON.parse(fs.readFileSync(CONFIG, 'utf8')) || {} } catch (e) { return {} }
}
function saveConfig(cfg) {
  try { fs.writeFileSync(CONFIG, JSON.stringify(cfg, null, 2), 'utf8') } catch (e) {}
}

function parseArgs() {
  const o = {}
  for (let i = 2; i < process.argv.length; i++) {
    if (process.argv[i] === '--server' && process.argv[i + 1]) o.server = process.argv[i + 1].replace(/\/+$/, '')
    if (process.argv[i] === '--shop' && process.argv[i + 1]) o.shop = Number(process.argv[i + 1]) || 0
    if (process.argv[i] === '--watch') o.watch = true
  }
  return o
}

async function api(server, p, shopId) {
  const h = { 'Content-Type': 'application/json' }
  if (shopId) h['X-Shop-Id'] = String(shopId)
  const r = await fetch(server + p, { headers: h, signal: AbortSignal.timeout(15000) })
  return r.json().catch(() => ({}))
}

// ---------- 主流程 ----------
async function main() {
  console.log('=============================================')
  console.log('  闲鱼一键绑定（通用版 · 真浏览器扫码）')
  console.log('=============================================')

  if (typeof fetch === 'undefined' || typeof WebSocket === 'undefined') {
    console.log('[错误] 需要 Node.js 18 或更高版本（内置 fetch / WebSocket）。')
    console.log('       到 https://nodejs.org 下载安装后再双击运行。')
    process.exit(1)
  }

  const args = parseArgs()
  const cfg = loadConfig()

  // 1) 服务器地址：命令行 > 配置 > 默认（首次引导）
  let server = args.server || cfg.server || DEFAULT_SERVER
  if (!args.server && !cfg.server) {
    console.log('')
    console.log('第一次使用，请填你的记账服务器地址（局域网或公网，例如 http://192.168.1.10:18083）')
    const input = await ask('  直接回车用默认 ' + DEFAULT_SERVER + '：')
    if (input) server = input.replace(/\/+$/, '')
    cfg.server = server
    saveConfig(cfg)
  }

  // 探测服务器 + 拉店铺列表
  let shops = []
  try {
    const d = await api(server, '/api/shops')
    shops = (d.items || []).filter((s) => !s.archived_at)
  } catch (e) {
    console.log('[错误] 连不上服务器 ' + server + '，或读取店铺列表失败。')
    console.log('       确认服务在运行、地址正确，然后重试（或双击后传 --server 参数）。')
    process.exit(1)
  }
  if (!shops.length) {
    console.log('[错误] 服务器上还没有店铺。请先在网页里建一个店铺。')
    process.exit(1)
  }

  // 2) 选店铺：命令行 --shop > 交互选择
  let shop
  if (args.shop) {
    shop = shops.find((s) => s.id === args.shop)
    if (!shop) { console.log('[错误] 找不到店铺 ID ' + args.shop); process.exit(1) }
  } else {
    console.log('')
    console.log('检测到这些店铺，请选择要绑定闲鱼账号到哪个店铺：')
    shops.forEach((s, i) => console.log('  [' + (i + 1) + '] ' + s.name + '  (ID ' + s.id + ')'))
    const pick = await ask('  输入序号（直接回车选第 1 个）：')
    const idx = pick ? (Number(pick) - 1) : 0
    if (isNaN(idx) || idx < 0 || idx >= shops.length) { console.log('[错误] 无效选择'); process.exit(1) }
    shop = shops[idx]
  }
  const shopId = shop.id
  console.log('')
  console.log('  → 目标店铺：' + shop.name + ' (ID ' + shopId + ')')
  console.log('  → 服务器：' + server)

  // 3) 浏览器探测
  const browser = findBrowser()
  if (!browser) {
    console.log('[错误] 没找到 Microsoft Edge 或 Google Chrome，请先安装其一。')
    process.exit(1)
  }
  console.log('  → 浏览器：' + path.basename(browser))

  // 4) 每店独立 profile，避免 Cookie 串号
  fs.mkdirSync(PROFILES, { recursive: true })
  const profile = path.join(PROFILES, 'shop-' + shopId)
  fs.mkdirSync(profile, { recursive: true })

  const PORT = 9400 + Math.floor(Math.random() * 500)
  const proc = spawn(browser, [
    '--remote-debugging-port=' + PORT,
    '--user-data-dir=' + profile,
    '--no-first-run', '--no-default-browser-check',
    '--window-size=1100,900', LOGIN
  ], { stdio: 'ignore', detached: false })

  let pageTarget = null
  for (let i = 0; i < 40; i++) {
    await sleep(400)
    try {
      const list = await (await fetch('http://127.0.0.1:' + PORT + '/json')).json()
      pageTarget = pickLoginPage(list)
      if (pageTarget) break
    } catch (e) {}
  }
  if (!pageTarget) {
    console.log('[错误] 浏览器启动失败，请重试。')
    try { proc.kill() } catch (e) {}
    process.exit(1)
  }

  const ws = new WebSocket(pageTarget.webSocketDebuggerUrl)
  let id = 0
  const pending = new Map()
  const send = (method, params = {}) => new Promise((res, rej) => {
    const i = ++id
    pending.set(i, { res, rej })
    ws.send(JSON.stringify({ id: i, method, params }))
  })
  ws.addEventListener('message', (e) => {
    const m = JSON.parse(e.data)
    if (m.id && pending.has(m.id)) {
      const q = pending.get(m.id)
      pending.delete(m.id)
      m.error ? q.rej(new Error(JSON.stringify(m.error))) : q.res(m.result)
    }
  })
  await new Promise((r) => ws.addEventListener('open', r, { once: true }))
  await send('Network.enable')
  await send('Page.enable')
  await send('Page.bringToFront').catch(() => {})
  await sleep(3500)

  // 尝试自动切到扫码 tab
  await send('Runtime.evaluate', {
    expression: `(() => { const el=[...document.querySelectorAll('*')].find(e=>e.children.length===0 && /扫码登录|扫一扫|二维码/.test(e.textContent.trim())); if(el){(el.closest('div,li,span,button,a')||el).click(); return true} return false })()`,
    returnByValue: true
  }).catch(() => {})

  async function readCookies(urls) {
    const { cookies } = await send('Network.getAllCookies')
    if (!cookies || !cookies.length) return ''
    const kept = cookies.filter((c) => c.name && c.value && urls.some((u) => c.domain && (c.domain === u || c.domain.endsWith('.' + u))))
    return kept.map((c) => c.name + '=' + c.value).join('; ')
  }
  async function readAll() {
    const goofish = await readCookies(['.goofish.com', 'goofish.com', '.passport.goofish.com'])
    const taobao = await readCookies(['.taobao.com', 'taobao.com', '.tmall.com', 'tmall.com'])
    return { goofish, taobao }
  }
  async function readUmid() {
    const r = await send('Runtime.evaluate', {
      expression: `(() => { try { for(let i=0;i<localStorage.length;i++){const k=localStorage.key(i); if(/umid/i.test(k)){const v=localStorage.getItem(k); if(v)return v}} } catch(e){} return '' })()`,
      returnByValue: true
    })
    return (r && r.result && r.result.value) || ''
  }
  async function tryBind() {
    const { goofish, taobao } = await readAll()
    if (!goofish.includes('cookie2=') && !goofish.includes('_m_h5_tk=')) {
      return { ok: false, error: '浏览器里还没有登录 Cookie' }
    }
    try {
      const r = await fetch(server + '/api/goofish/rebind', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Shop-Id': String(shopId) },
        body: JSON.stringify({ goofish, taobao, umid_token: await readUmid() }),
        signal: AbortSignal.timeout(25000)
      })
      const d = await r.json().catch(() => ({}))
      if (r.ok && d.ok) return d
      return { ok: false, error: d.error || ('服务器返回 ' + r.status) }
    } catch (e) { return { ok: false, error: e.message || '连接服务器失败' } }
  }

  async function readStatus() {
    const r = await fetch(server + '/api/goofish/status', {
      headers: { 'X-Shop-Id': String(shopId) },
      signal: AbortSignal.timeout(15000)
    })
    if (!r.ok) throw new Error('状态接口返回 ' + r.status)
    return r.json()
  }

  async function heartbeat() {
    const r = await fetch(server + '/api/goofish/bridge/heartbeat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Shop-Id': String(shopId) },
      body: '{}',
      signal: AbortSignal.timeout(15000)
    })
    if (!r.ok) throw new Error('心跳接口返回 ' + r.status)
  }

  async function closeBrowser() {
    try { await send('Browser.close') } catch (e) {}
    try { proc.kill() } catch (e) {}
  }

  console.log('')
  console.log('已打开闲鱼登录页，请用手机闲鱼扫屏幕上的二维码并在手机上确认。')
  console.log('（这个专用窗口只属于「' + shop.name + '」，不会影响你日常浏览器）')
  console.log('等待扫码绑定…（最长 10 分钟）')

  const deadline = Date.now() + 10 * 60 * 1000
  let hinted = false
  let waitingHeartbeatAt = 0
  while (Date.now() < deadline) {
    await sleep(3000)
    // 保持浏览器桥在“等待扫码”阶段就显示在线，否则用户刚启动时
    // 页面仍提示离线，无法判断窗口到底有没有接管。
    if (args.watch && Date.now() - waitingHeartbeatAt >= STATUS_TICK) {
      await heartbeat().catch(() => {})
      waitingHeartbeatAt = Date.now()
    }
    const d = await tryBind()
    if (d && d.ok) {
      console.log('')
      console.log('  ✓ 绑定成功！' + shop.name + ' ← ' + (d.nick || ''))
      console.log('    淘宝 SSO：' + (d.sso_saved ? '已保存（后台检查用）' : '未拿到（不影响本次使用）'))
      console.log('')
      if (!args.watch) {
        await closeBrowser()
        process.exit(0)
      }
      break
    }
    if (!hinted) { hinted = true; console.log('   （等待扫码…请用手机扫浏览器里的二维码并点确认）') }
  }
  if (!args.watch) {
    console.log('[超时] 10 分钟内未检测到登录，请重新运行再试。')
    await closeBrowser()
    process.exit(1)
  }

  // ---------- 浏览器保活桥 ----------
  // 保持专用真实浏览器常驻，域名 Cookie 更新后自动上报并让服务端校验。
  // 这正是服务器裸 HTTP 做不到的部分：cookie2 需要由真实浏览器登录环境产生。
  console.log('')
  console.log('  浏览器保活桥已启动')
  console.log('  这个窗口可以最小化，但不要关闭；关闭后只停止自动换新 Cookie。')
  console.log('  登录失效时，在窗口里重新扫码即可，新 Cookie 会自动上报。')
  console.log('')

  let current = await readAll()
  let uploadedFingerprint = authFingerprint(current.goofish, current.taobao)
  let lastStatus = null
  let lastStatusAt = 0
  let lastHeartbeatAt = 0
  let lastAttemptAt = Date.now()
  let lastNavigateAt = Date.now()
  let lastNotice = ''
  await send('Page.navigate', { url: 'https://www.goofish.com/' }).catch(() => {})

  const setNotice = (msg) => {
    if (!msg || msg === lastNotice) return
    lastNotice = msg
    console.log(new Date().toLocaleTimeString() + '  ' + msg)
  }

  while (true) {
    try {
      const now = Date.now()
      current = await readAll()
      const fingerprint = authFingerprint(current.goofish, current.taobao)
      const changed = !!fingerprint && fingerprint !== uploadedFingerprint

      if (now - lastStatusAt >= STATUS_TICK) {
        lastStatus = await readStatus()
        lastStatusAt = now
      }
      if (now - lastHeartbeatAt >= STATUS_TICK) {
        await heartbeat()
        lastHeartbeatAt = now
      }

      const invalid = statusNeedsBinding(lastStatus)
      const retryDelay = changed ? 1_000 : FAILED_UPLOAD_RETRY
      if (fingerprint && (changed || invalid) && now - lastAttemptAt >= retryDelay) {
        lastAttemptAt = now
        const d = await tryBind()
        if (d && d.ok) {
          uploadedFingerprint = fingerprint
          console.log(new Date().toLocaleTimeString() + '  ✓ 浏览器登录态已上报并通过订单接口校验')
          lastStatus = await readStatus().catch(() => null)
          lastStatusAt = Date.now()
        } else {
          setNotice('当前登录态暂不可用，请在专用窗口中重新登录/扫码；' + ((d && d.error) ? d.error : '稍后自动重试'))
        }
      }

      if (!invalid && now - lastNavigateAt >= PAGE_REFRESH) {
        await send('Page.navigate', { url: 'https://www.goofish.com/' }).catch(() => {})
        lastNavigateAt = now
        setNotice('已轻量刷新闲鱼页面，继续等待登录态更新')
      } else if (!invalid) {
        setNotice('订单接口可用，浏览器桥正在守护登录态')
      }
    } catch (e) {
      setNotice('保活桥暂时连不上服务，稍后自动重试：' + (e.message || e))
    }
    await sleep(WATCH_TICK)
  }
}

if (require.main === module) {
  main().catch((e) => { console.error('[错误]', e.message); process.exit(1) })
}

module.exports = { pickLoginPage, authFingerprint, statusNeedsBinding }

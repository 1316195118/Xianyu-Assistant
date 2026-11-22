const DEFAULT_CONFIG = {
  server: 'http://10.10.10.251:18083',
  shopId: 1,
  bridgeToken: ''
}

const ALARM_NAME = 'goofish-cookie-sync'
const RETRY_COOLDOWN_MS = 15 * 60 * 1000
const AUTH_COOKIE_NAMES = new Set([
  'cookie2', 'sgcookie', 'unb', 'cna', 't', '_tb_token_', '_m_h5_tk',
  'cookie17', 'lgc', 'tracknick', '_nk_', 'havana_lgc_exp',
  'cookie3_bak_exp', 'x5sec'
])

let syncing = null
let pendingTimer = null

async function getConfig() {
  const saved = await chrome.storage.local.get(DEFAULT_CONFIG)
  return {
    server: String(saved.server || DEFAULT_CONFIG.server).replace(/\/+$/, ''),
    shopId: Number(saved.shopId) || DEFAULT_CONFIG.shopId,
    bridgeToken: String(saved.bridgeToken || '').trim()
  }
}

function serverOrigin(server) {
  return new URL(server).origin
}

async function ensureServerPermission(server) {
  const origin = serverOrigin(server) + '/*'
  if (await chrome.permissions.contains({ origins: [origin] })) return true
  return false
}

async function requestJSON(path, options = {}) {
  const cfg = await getConfig()
  if (!await ensureServerPermission(cfg.server)) {
    throw new Error('尚未授权访问记账服务器，请打开扩展设置并点击保存')
  }
  const headers = {
    'Content-Type': 'application/json',
    'X-Shop-Id': String(cfg.shopId),
    ...(options.headers || {})
  }
  if (cfg.bridgeToken) headers['X-Bridge-Token'] = cfg.bridgeToken
  const response = await fetch(cfg.server + path, {
    credentials: 'include',
    ...options,
    headers
  })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) {
    if (response.status === 401 && !cfg.bridgeToken) {
      throw new Error('鱼助手需要扩展配对码：请到「闲鱼同步」生成后填入扩展设置')
    }
    if (response.status === 401) {
      throw new Error('扩展配对码无效或已被撤销，请重新生成并保存')
    }
    throw new Error(data.error || ('服务器返回 ' + response.status))
  }
  return data
}

async function readCookieJar(url) {
  const list = await chrome.cookies.getAll({ url })
  const byName = new Map()
  for (const cookie of list) {
    if (cookie.name && cookie.value) byName.set(cookie.name, cookie.value)
  }
  return byName
}

async function readLoginCookies() {
  const goofish = await readCookieJar('https://www.goofish.com/')
  const taobao = await readCookieJar('https://www.taobao.com/')
  const tmall = await readCookieJar('https://www.tmall.com/')
  const sso = new Map([...taobao, ...tmall])
  return {
    goofish,
    sso,
    goofishRaw: [...goofish].map(([name, value]) => name + '=' + value).join('; '),
    ssoRaw: [...sso].map(([name, value]) => name + '=' + value).join('; ')
  }
}

async function fingerprint(goofish, sso) {
  const picked = [
    ...[...goofish].filter(([name]) => AUTH_COOKIE_NAMES.has(name)).map(([name, value]) => 'g:' + name + '=' + value),
    ...[...sso].filter(([name]) => AUTH_COOKIE_NAMES.has(name)).map(([name, value]) => 't:' + name + '=' + value)
  ].sort().join('\n')
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(picked))
  return [...new Uint8Array(digest)].map((n) => n.toString(16).padStart(2, '0')).join('')
}

function needsBinding(status) {
  return !status || !status.logged_in || !!status.expired || !!status.sync_error
}

async function setBadge(ok, detail) {
  await chrome.action.setBadgeText({ text: ok ? '✓' : '!' })
  await chrome.action.setBadgeBackgroundColor({ color: ok ? '#2e7d32' : '#c62828' })
  await chrome.action.setTitle({ title: '鱼助手 · 闲鱼登录同步\n' + detail })
}

async function syncNow(force = false) {
  if (syncing) return syncing
  syncing = (async () => {
    let heartbeatOK = false
    try {
      const status = await requestJSON('/api/goofish/status')
      await requestJSON('/api/goofish/bridge/heartbeat', { method: 'POST', body: '{}' })
      heartbeatOK = true

      const jar = await readLoginCookies()
      if (!jar.goofishRaw) {
        await setBadge(false, '浏览器里还没有闲鱼登录 Cookie，请先登录 www.goofish.com')
        return { ok: false, heartbeatOK, message: '请先在当前浏览器登录闲鱼' }
      }

      const fp = await fingerprint(jar.goofish, jar.sso)
      const stored = await chrome.storage.local.get({
        lastUploadedFingerprint: '',
        lastAttemptedFingerprint: '',
        lastAttemptAt: 0,
        lastResult: ''
      })
      if (!force && fp === stored.lastUploadedFingerprint) {
        const detail = needsBinding(status)
          ? '浏览器登录凭证已上报，但服务端尚未恢复；请检查服务端提示'
          : '订单接口可用，登录态未变化'
        await setBadge(!needsBinding(status), detail)
        return { ok: !needsBinding(status), heartbeatOK, message: detail }
      }
      const coolingDown = fp === stored.lastAttemptedFingerprint &&
        Date.now() - Number(stored.lastAttemptAt || 0) < RETRY_COOLDOWN_MS
      if (!force && coolingDown) {
        const minutes = Math.max(1, Math.ceil(
          (RETRY_COOLDOWN_MS - (Date.now() - Number(stored.lastAttemptAt || 0))) / 60000
        ))
        const message = '本次登录态校验失败，' + minutes + ' 分钟后再试；若已重新登录请立即点“立即检查并上报”'
        await setBadge(false, message)
        return { ok: false, heartbeatOK, message }
      }
      await chrome.storage.local.set({
        lastAttemptedFingerprint: fp,
        lastAttemptAt: Date.now()
      })
      const result = await requestJSON('/api/goofish/rebind', {
        method: 'POST',
        body: JSON.stringify({
          goofish: jar.goofishRaw,
          taobao: jar.ssoRaw,
          umid_token: ''
        })
      })
      await chrome.storage.local.set({
        lastUploadedFingerprint: fp,
        lastResult: 'ok',
        lastSyncAt: new Date().toISOString(),
        lastNick: result.nick || ''
      })
      await setBadge(true, '已自动上报并通过订单接口校验')
      return { ok: true, heartbeatOK, message: '已自动上报并通过校验', nick: result.nick || '' }
    } catch (error) {
      const message = error && error.message ? error.message : String(error)
      await chrome.storage.local.set({ lastResult: 'error', lastError: message, lastSyncAt: new Date().toISOString() })
      await setBadge(false, message)
      return { ok: false, heartbeatOK, message }
    } finally {
      syncing = null
    }
  })()
  return syncing
}

function debounceSync(ms = 1800) {
  if (pendingTimer) clearTimeout(pendingTimer)
  pendingTimer = setTimeout(() => {
    pendingTimer = null
    syncNow(false)
  }, ms)
}

async function setup() {
  const existing = await chrome.alarms.get(ALARM_NAME)
  if (!existing) await chrome.alarms.create(ALARM_NAME, { periodInMinutes: 1 })
  await syncNow(false)
}

chrome.runtime.onInstalled.addListener(async () => {
  await setup()
  await chrome.runtime.openOptionsPage()
})

chrome.runtime.onStartup.addListener(setup)

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === ALARM_NAME) syncNow(false)
})

chrome.cookies.onChanged.addListener((change) => {
  if (change.removed || !AUTH_COOKIE_NAMES.has(change.cookie.name)) return
  debounceSync()
})

chrome.storage.onChanged.addListener((changes, area) => {
  if (area === 'local' && (changes.server || changes.shopId)) debounceSync(300)
})

chrome.action.onClicked.addListener(() => chrome.runtime.openOptionsPage())

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message && message.type === 'sync-now') {
    syncNow(true).then(sendResponse)
    return true
  }
  if (message && message.type === 'get-runtime-status') {
    Promise.all([
      chrome.storage.local.get({ lastSyncAt: '', lastResult: '', lastError: '', lastNick: '' }),
      getConfig()
    ]).then(([saved, cfg]) => sendResponse({ ok: true, saved, config: cfg }))
    return true
  }
})

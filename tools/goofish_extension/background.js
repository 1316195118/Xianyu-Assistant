const DEFAULT_CONFIG = {
  server: 'http://10.10.10.251:18083',
  shopId: 1,
  bridgeToken: '',
  autoRefreshLogin: true
}

const ALARM_NAME = 'goofish-cookie-sync'
const UPDATE_ALARM_NAME = 'goofish-extension-update-check'
const UPDATE_CHECK_INTERVAL_MS = 24 * 60 * 60 * 1000
const UPDATE_MANIFEST_PATH = '/downloads/goofish-extension-version.json'
const RETRY_COOLDOWN_MS = 15 * 60 * 1000
const LOGIN_REFRESH_INTERVAL_MS = 4 * 60 * 60 * 1000
const LOGIN_REFRESH_RETRY_MS = 30 * 60 * 1000
const LOGIN_REFRESH_TIMEOUT_MS = 25 * 1000
const LOGIN_REFRESH_SETTLE_MS = 350
const LOGIN_REFRESH_URL = 'https://www.goofish.com/?xy_ext_refresh=1'
const INTERACTIVE_LOGIN_URL = 'https://www.goofish.com/login'
const INTERACTIVE_LOGIN_WINDOW_MS = 10 * 60 * 1000
const INTERACTIVE_QR_RETRIES = 16
const INTERACTIVE_QR_RETRY_MS = 650
const AUTH_COOKIE_NAMES = new Set([
  'cookie2', 'sgcookie', 'unb', 'cna', 't', '_tb_token_', '_m_h5_tk',
  'cookie17', 'lgc', 'tracknick', '_nk_', 'havana_lgc_exp',
  'cookie3_bak_exp', 'x5sec'
])

let syncing = null
let refreshing = null
let updateChecking = null
let connectionChecking = null
let pendingTimer = null
let interactiveLoginTimer = null

async function getConfig() {
  const saved = await chrome.storage.local.get(DEFAULT_CONFIG)
  return {
    server: String(saved.server || DEFAULT_CONFIG.server).replace(/\/+$/, ''),
    shopId: Number(saved.shopId) || DEFAULT_CONFIG.shopId,
    bridgeToken: String(saved.bridgeToken || '').trim(),
    autoRefreshLogin: saved.autoRefreshLogin !== false
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
    throw new Error('尚未授权访问记账服务器，请打开扩展快捷面板并点击“保存并连接”')
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
    let message = data.error || ('服务器返回 ' + response.status)
    if (response.status === 401 && !cfg.bridgeToken) {
      message = '鱼助手需要扩展配对码：请到「闲鱼同步」生成后填入扩展快捷面板'
    } else if (response.status === 401) {
      message = '扩展配对码无效或已被撤销，请在快捷面板中重新填入并保存'
    }
    const error = new Error(message)
    error.status = response.status
    throw error
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

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function extractLoginQr(tabId) {
  if (!chrome.scripting || typeof chrome.scripting.executeScript !== 'function') {
    return { ok: false, message: '当前浏览器不支持扩展内提取二维码' }
  }
  let results
  try {
    results = await chrome.scripting.executeScript({
      target: { tabId, allFrames: true },
      func: () => {
        const dataUrl = (value) => typeof value === 'string' && /^data:image\//i.test(value) && value.length > 500

        function canvasUrl(canvas) {
          if (!canvas) return ''
          const width = Number(canvas.width || canvas.clientWidth || 0)
          const height = Number(canvas.height || canvas.clientHeight || 0)
          if (width < 80 || height < 80) return ''
          try {
            const value = canvas.toDataURL('image/png')
            return dataUrl(value) ? value : ''
          } catch (error) {
            return ''
          }
        }

        function imageUrl(img) {
          if (!img) return ''
          const width = Number(img.naturalWidth || img.clientWidth || 0)
          const height = Number(img.naturalHeight || img.clientHeight || 0)
          if (width < 80 || height < 80) return ''
          const src = String(img.currentSrc || img.src || '')
          if (dataUrl(src)) return src
          try {
            const canvas = document.createElement('canvas')
            canvas.width = width
            canvas.height = height
            const context = canvas.getContext('2d')
            context.drawImage(img, 0, 0, width, height)
            const value = canvas.toDataURL('image/png')
            return dataUrl(value) ? value : ''
          } catch (error) {
            return ''
          }
        }

        function backgroundUrl(node) {
          const background = getComputedStyle(node).backgroundImage || ''
          const match = background.match(/url\(["']?(data:image\/[^"')]+)["']?\)/i)
          return match && dataUrl(match[1]) ? match[1] : ''
        }

        const candidates = [...document.querySelectorAll('canvas, img, svg, [style*="background-image"]')]
        candidates.sort((left, right) => {
          const a = left.getBoundingClientRect()
          const b = right.getBoundingClientRect()
          const score = (rect) => Math.min(rect.width, rect.height) *
            (Math.abs(rect.width - rect.height) < Math.max(rect.width, rect.height) * 0.2 ? 2 : 1)
          return score(b) - score(a)
        })
        for (const node of candidates) {
          let value = ''
          if (node.tagName === 'CANVAS') value = canvasUrl(node)
          else if (node.tagName === 'IMG') value = imageUrl(node)
          else if (node.tagName === 'svg' || node.tagName === 'SVG') {
            const rect = node.getBoundingClientRect()
            if (rect.width >= 80 && rect.height >= 80) {
              try {
                const xml = new XMLSerializer().serializeToString(node)
                value = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(xml)
              } catch (error) {}
            }
          }
          if (!value) value = backgroundUrl(node)
          if (dataUrl(value)) {
            return { ok: true, dataUrl: value, source: node.tagName.toLowerCase() }
          }
        }
        return {
          ok: false,
          message: document.querySelector('iframe')
            ? '官方登录页仍在加载二维码'
            : '没有在官方登录页找到二维码元素'
        }
      }
    })
  } catch (error) {
    return { ok: false, message: error && error.message ? error.message : String(error) }
  }
  for (const item of results || []) {
    if (item && item.result && item.result.ok && item.result.dataUrl) return item.result
  }
  const first = (results || []).find((item) => item && item.result && item.result.message)
  return { ok: false, message: first ? first.result.message : '没有找到闲鱼登录二维码' }
}

async function captureInteractiveQr(tabId) {
  for (let attempt = 0; attempt < INTERACTIVE_QR_RETRIES; attempt++) {
    if (attempt > 0) await delay(INTERACTIVE_QR_RETRY_MS)
    const result = await extractLoginQr(tabId)
    if (result && result.ok && result.dataUrl) {
      await chrome.storage.local.set({
        interactiveLoginQr: result.dataUrl,
        interactiveLoginQrCapturedAt: Date.now(),
        interactiveLoginQrError: ''
      })
      return result
    }
  }
  const message = '没有读取到官方二维码，已切换到官方登录页'
  await chrome.storage.local.set({
    interactiveLoginQr: '',
    interactiveLoginQrCapturedAt: 0,
    interactiveLoginQrError: message
  })
  return { ok: false, message }
}

function compareVersions(left, right) {
  const a = String(left || '').split('.').map((part) => Number.parseInt(part, 10) || 0)
  const b = String(right || '').split('.').map((part) => Number.parseInt(part, 10) || 0)
  const length = Math.max(a.length, b.length)
  for (let index = 0; index < length; index++) {
    const diff = (a[index] || 0) - (b[index] || 0)
    if (diff !== 0) return diff > 0 ? 1 : -1
  }
  return 0
}

async function checkExtensionUpdate(force = false) {
  if (updateChecking) return updateChecking
  updateChecking = (async () => {
    const cfg = await getConfig()
    const currentVersion = chrome.runtime.getManifest().version
    const saved = await chrome.storage.local.get({
      lastExtensionUpdateCheckAt: '',
      extensionLatestVersion: '',
      extensionCheckedVersion: '',
      extensionUpdateAvailable: false,
      extensionDownloadUrl: '',
      extensionUpdateError: ''
    })
    const lastCheckAt = Date.parse(saved.lastExtensionUpdateCheckAt || '') || 0
    if (!force && saved.extensionCheckedVersion === currentVersion &&
        Date.now() - lastCheckAt < UPDATE_CHECK_INTERVAL_MS) {
      return {
        ok: !saved.extensionUpdateError,
        checked: false,
        currentVersion,
        latestVersion: saved.extensionLatestVersion,
        updateAvailable: !!saved.extensionUpdateAvailable,
        downloadUrl: saved.extensionDownloadUrl,
        message: saved.extensionUpdateError || (saved.extensionUpdateAvailable
          ? '发现扩展新版 ' + saved.extensionLatestVersion
          : '扩展已是最新版本')
      }
    }
    try {
      if (!await ensureServerPermission(cfg.server)) {
        throw new Error('尚未授权访问鱼助手服务器，请先保存设置')
      }
      const response = await fetch(cfg.server + UPDATE_MANIFEST_PATH, {
        cache: 'no-store',
        credentials: 'omit',
        signal: AbortSignal.timeout(10000)
      })
      const data = await response.json().catch(() => ({}))
      if (!response.ok || !data.version) {
        throw new Error(data.error || ('版本检查返回 ' + response.status))
      }
      const latestVersion = String(data.version).trim()
      const updateAvailable = compareVersions(latestVersion, currentVersion) > 0
      const downloadPath = String(data.download_path || '/downloads/goofish-extension.zip')
      const downloadUrl = new URL(downloadPath, cfg.server + '/').toString()
      const checkedAt = new Date().toISOString()
      await chrome.storage.local.set({
        lastExtensionUpdateCheckAt: checkedAt,
        extensionLatestVersion: latestVersion,
        extensionCheckedVersion: currentVersion,
        extensionUpdateAvailable: updateAvailable,
        extensionDownloadUrl: downloadUrl,
        extensionLatestSha256: String(data.sha256 || ''),
        extensionUpdateError: ''
      })
      if (updateAvailable) {
        await chrome.action.setBadgeText({ text: '↑' })
        await chrome.action.setBadgeBackgroundColor({ color: '#d97706' })
        await chrome.action.setTitle({
          title: '鱼助手 · 闲鱼登录同步\n发现扩展新版 ' + latestVersion + '，请打开设置下载并重新加载'
        })
      }
      return {
        ok: true,
        checked: true,
        currentVersion,
        latestVersion,
        updateAvailable,
        downloadUrl,
        checkedAt,
        message: updateAvailable ? '发现扩展新版 ' + latestVersion : '扩展已是最新版本 ' + currentVersion
      }
    } catch (error) {
      const message = error && error.message ? error.message : String(error)
      await chrome.storage.local.set({
        lastExtensionUpdateCheckAt: new Date().toISOString(),
        extensionUpdateError: message
      })
      return {
        ok: false,
        checked: true,
        currentVersion,
        latestVersion: saved.extensionLatestVersion,
        updateAvailable: !!saved.extensionUpdateAvailable,
        downloadUrl: saved.extensionDownloadUrl,
        message: '检查扩展更新失败：' + message
      }
    } finally {
      updateChecking = null
    }
  })()
  return updateChecking
}

async function checkConnection() {
  if (connectionChecking) return connectionChecking
  connectionChecking = (async () => {
    const cfg = await getConfig()
    const checkedAt = new Date().toISOString()
    if (!await ensureServerPermission(cfg.server)) {
      return {
        ok: false,
        reachable: false,
        bridgeOK: false,
        usable: false,
        checkedAt,
        message: '尚未授权访问鱼助手服务器，请在快捷面板点击“保存并连接”'
      }
    }

    let status
    try {
      status = await requestJSON('/api/goofish/status')
    } catch (error) {
      return {
        ok: false,
        reachable: Number(error && error.status) > 0,
        bridgeOK: false,
        usable: false,
        checkedAt,
        message: error.message || '无法连接鱼助手服务器'
      }
    }

    let bridgeOK = false
    let bridgeError = ''
    try {
      await requestJSON('/api/goofish/bridge/heartbeat', { method: 'POST', body: '{}' })
      bridgeOK = true
    } catch (error) {
      bridgeError = error.message || '扩展配对校验失败'
    }

    const sessionValid = !!status.session_valid && !status.expired
    const orderAPIValid = !!status.order_api_valid && !status.sync_error
    const usable = bridgeOK && sessionValid && orderAPIValid
    let message = '服务器、配对码和闲鱼订单接口均正常'
    if (!bridgeOK) {
      message = bridgeError
    } else if (!status.logged_in) {
      message = '服务器已连接，但还没有绑定闲鱼账号'
    } else if (status.expired || !status.session_valid) {
      message = '服务器已连接，但闲鱼登录态已失效，需要重新登录'
    } else if (status.sync_error) {
      message = '服务器已连接，但闲鱼订单接口校验失败：' + status.sync_error
    } else if (!orderAPIValid) {
      message = '服务器已连接，闲鱼凭证已保存，订单接口尚待验证'
    }

    return {
      ok: usable,
      reachable: true,
      bridgeOK,
      usable,
      checkedAt,
      message,
      status: {
        shop_id: status.shop_id || cfg.shopId,
        shop_name: status.shop_name || '',
        logged_in: !!status.logged_in,
        session_valid: sessionValid,
        order_api_valid: orderAPIValid,
        expired: !!status.expired,
        sync_error: status.sync_error || '',
        nick: status.nick || '',
        last_sync: status.last_sync || '',
        masked: status.masked || ''
      }
    }
  })()
  try {
    const result = await connectionChecking
    // 面板状态与工具栏角标共用同一份实时连接结果：面板显示「当前可用」时，
    // 角标必须同步为 ✓。此前角标只在 syncNow 的旁路逻辑里更新，服务端状态
    // 已恢复（或面板刚打开）时角标仍是空白/旧值，出现「状态正常但不显示」。
    await syncBadgeFromConnection(result)
    return result
  } finally {
    connectionChecking = null
  }
}

async function setBadge(ok, detail) {
  const saved = await chrome.storage.local.get({
    extensionUpdateAvailable: false,
    extensionLatestVersion: ''
  })
  if (saved.extensionUpdateAvailable) {
    await chrome.action.setBadgeText({ text: '↑' })
    await chrome.action.setBadgeBackgroundColor({ color: '#d97706' })
    await chrome.action.setTitle({
      title: '鱼助手 · 闲鱼登录同步\n发现扩展新版 ' + saved.extensionLatestVersion +
        '；' + detail
    })
    return
  }
  await chrome.action.setBadgeText({ text: ok ? '✓' : '!' })
  await chrome.action.setBadgeBackgroundColor({ color: ok ? '#2e7d32' : '#c62828' })
  await chrome.action.setTitle({ title: '鱼助手 · 闲鱼登录同步\n' + detail })
}

// 工具栏角标与快捷面板统一以实时连接结果为准。
// 后台续期（↻）和扫码登录进行中时保留各自的临时角标，不被一次状态查询覆盖。
async function syncBadgeFromConnection(result) {
  if (!result) return
  if (refreshing) return
  const saved = await chrome.storage.local.get({
    interactiveLoginStartedAt: 0,
    interactiveLoginState: ''
  })
  if (interactiveLoginActive(saved)) return
  await setBadge(!!result.usable, result.message || '鱼助手状态检查完成')
}

function waitForTabComplete(tabId, timeoutMs) {
  return new Promise((resolve) => {
    let settled = false
    let timer = null
    const finish = (loaded) => {
      if (settled) return
      settled = true
      if (timer) clearTimeout(timer)
      chrome.tabs.onUpdated.removeListener(onUpdated)
      resolve(loaded)
    }
    const onUpdated = (updatedTabId, changeInfo) => {
      if (updatedTabId === tabId && changeInfo && changeInfo.status === 'complete') finish(true)
    }
    chrome.tabs.onUpdated.addListener(onUpdated)
    timer = setTimeout(() => finish(false), timeoutMs)
    chrome.tabs.get(tabId).then((tab) => {
      if (tab && tab.status === 'complete') finish(true)
    }).catch(() => {})
  })
}

async function cleanupOrphanRefreshTab() {
  const saved = await chrome.storage.local.get({ loginRefreshTabId: 0 })
  const tabId = Number(saved.loginRefreshTabId) || 0
  if (!tabId) return
  try {
    const tab = await chrome.tabs.get(tabId)
    const markedRefreshTab = /^https:\/\/(?:www\.)?goofish\.com\/\?xy_ext_refresh=1(?:[&#]|$)/i.test(tab.url || '')
    if (tab && markedRefreshTab) {
      await chrome.tabs.remove(tabId)
    }
  } catch (error) {
    // 标签页已关闭或浏览器重启过，清理记录即可。
  }
  await chrome.storage.local.set({ loginRefreshTabId: 0, loginRefreshStartedAt: 0 })
}

// 在非活动标签页中访问一次闲鱼首页，让浏览器按真实页面规则续期 Cookie。
// 只关闭扩展自己创建的临时标签，绝不刷新或关闭用户正在使用的闲鱼页面。
async function refreshLoginSession(reason = 'scheduled') {
  if (refreshing) return refreshing
  refreshing = (async () => {
    const startedAt = Date.now()
    let tabId = 0
    try {
      await chrome.action.setBadgeText({ text: '↻' })
      await chrome.action.setBadgeBackgroundColor({ color: '#b77900' })
      await chrome.action.setTitle({ title: '鱼助手 · 闲鱼登录同步\n正在后台续期登录态' })
      await chrome.storage.local.set({
        lastLoginRefreshAt: new Date(startedAt).toISOString(),
        lastLoginRefreshResult: 'running',
        lastLoginRefreshMessage: reason === 'recovery' ? '登录态异常，正在后台恢复' : '正在后台续期登录态'
      })
      const tab = await chrome.tabs.create({ url: LOGIN_REFRESH_URL, active: false })
      tabId = Number(tab && tab.id) || 0
      if (!tabId) throw new Error('浏览器没有返回后台标签页')
      await chrome.storage.local.set({ loginRefreshTabId: tabId, loginRefreshStartedAt: startedAt })
      const loaded = await waitForTabComplete(tabId, LOGIN_REFRESH_TIMEOUT_MS)
      await delay(LOGIN_REFRESH_SETTLE_MS)
      const jar = await readLoginCookies()
      const fp = await fingerprint(jar.goofish, jar.sso)
      const message = loaded ? '后台续期完成' : '后台页面加载超时，已读取当前登录态'
      await chrome.storage.local.set({
        lastLoginRefreshResult: loaded ? 'ok' : 'timeout',
        lastLoginRefreshMessage: message
      })
      return { ok: loaded, jar, fingerprint: fp, message }
    } catch (error) {
      const message = error && error.message ? error.message : String(error)
      await chrome.storage.local.set({
        lastLoginRefreshResult: 'error',
        lastLoginRefreshMessage: message
      })
      return { ok: false, jar: null, fingerprint: '', message }
    } finally {
      if (tabId) {
        try { await chrome.tabs.remove(tabId) } catch (error) {}
      }
      await chrome.storage.local.set({ loginRefreshTabId: 0, loginRefreshStartedAt: 0 })
      refreshing = null
    }
  })()
  return refreshing
}

function interactiveLoginActive(saved) {
  const startedAt = Number(saved.interactiveLoginStartedAt || 0)
  return startedAt > 0 && Date.now() - startedAt < INTERACTIVE_LOGIN_WINDOW_MS &&
    !['success', 'failed'].includes(saved.interactiveLoginState)
}

async function setInteractiveLoginState(state, message, extra = {}) {
  await chrome.storage.local.set({
    interactiveLoginState: state,
    interactiveLoginMessage: message,
    interactiveLoginUpdatedAt: new Date().toISOString(),
    ...extra
  })
}

async function completeInteractiveLogin(ok, message, nick = '') {
  const saved = await chrome.storage.local.get({
    interactiveLoginStartedAt: 0,
    interactiveLoginState: '',
    interactiveLoginCookieChangedAt: 0
  })
  if (!saved.interactiveLoginStartedAt ||
      Date.now() - Number(saved.interactiveLoginStartedAt || 0) >= INTERACTIVE_LOGIN_WINDOW_MS) {
    return
  }
  if (ok) {
    await setInteractiveLoginState('success', message || '扫码登录成功，登录态已上报并通过校验', {
      interactiveLoginNick: nick,
      interactiveLoginFinishedAt: Date.now(),
      interactiveLoginQr: '',
      interactiveLoginQrError: ''
    })
    await setBadge(true, message || '扫码登录成功')
    await closeTrackedInteractiveLoginTab()
    return
  }
  if (!saved.interactiveLoginCookieChangedAt) return
  await setInteractiveLoginState('failed', message || '登录未完成，请重试', {
    interactiveLoginFinishedAt: Date.now()
  })
}

async function verifyInteractiveLogin() {
  const saved = await chrome.storage.local.get({
    interactiveLoginStartedAt: 0,
    interactiveLoginState: '',
    interactiveLoginCookieChangedAt: 0,
    interactiveLoginBaselineFingerprint: ''
  })
  if (!interactiveLoginActive(saved)) return

  const jar = await readLoginCookies()
  if (!jar.goofish.has('cookie2') && !jar.goofish.has('unb')) {
    await setInteractiveLoginState('waiting', '官方登录页已打开，请用闲鱼 App 扫码并确认')
    return
  }
  const currentFingerprint = await fingerprint(jar.goofish, jar.sso)
  const changed = !saved.interactiveLoginCookieChangedAt &&
    currentFingerprint !== saved.interactiveLoginBaselineFingerprint
  if (!saved.interactiveLoginCookieChangedAt && !changed) {
    await setInteractiveLoginState('waiting', '官方登录页已打开，请用闲鱼 App 扫码并确认')
    return
  }
  await setInteractiveLoginState('verifying', '已检测到登录完成，正在校验并上报…', {
    interactiveLoginCookieChangedAt: Date.now()
  })
  const result = await syncNow(false)
  if (result.ok) {
    await completeInteractiveLogin(true, '扫码登录成功，登录态已上报并通过校验', result.nick || '')
  }
}

async function closeTrackedInteractiveLoginTab() {
  const saved = await chrome.storage.local.get({ interactiveLoginTabId: 0 })
  const tabId = Number(saved.interactiveLoginTabId || 0)
  if (tabId) {
    try {
      const tab = await chrome.tabs.get(tabId)
      if (tab && /^https:\/\/(?:www\.)?goofish\.com\/login(?:[?#]|$)/i.test(tab.url || '')) {
        await chrome.tabs.remove(tabId)
      }
    } catch (error) {
      // 标签已经关闭时只清理记录。
    }
  }
  await chrome.storage.local.set({ interactiveLoginTabId: 0 })
}

async function noteInteractiveLoginCookieChange() {
  const saved = await chrome.storage.local.get({
    interactiveLoginStartedAt: 0,
    interactiveLoginState: ''
  })
  if (!interactiveLoginActive(saved)) return
  await setInteractiveLoginState('verifying', '已检测到登录完成，正在校验并上报…', {
    interactiveLoginCookieChangedAt: Date.now()
  })
}

async function armInteractiveLogin() {
  const baselineJar = await readLoginCookies()
  const baselineFingerprint = await fingerprint(baselineJar.goofish, baselineJar.sso)
  await chrome.storage.local.set({
    interactiveLoginStartedAt: Date.now(),
    interactiveLoginState: 'waiting',
    interactiveLoginMessage: '官方登录页已打开，请用闲鱼 App 扫码并确认',
    interactiveLoginCookieChangedAt: 0,
    interactiveLoginFinishedAt: 0,
    interactiveLoginNick: '',
    interactiveLoginQr: '',
    interactiveLoginQrCapturedAt: 0,
    interactiveLoginQrError: '',
    interactiveLoginBaselineFingerprint: baselineFingerprint
  })
  await setBadge(true, '已打开闲鱼官方登录页，等待扫码完成')
  if (interactiveLoginTimer) clearTimeout(interactiveLoginTimer)
  interactiveLoginTimer = setTimeout(() => {
    interactiveLoginTimer = null
    verifyInteractiveLogin().catch(() => {})
  }, 2500)
  return {
    ok: true,
    message: '官方登录页已打开，请用闲鱼 App 扫码并确认'
  }
}

async function openInteractiveLogin(scheduleVerification = true) {
  const cfg = await getConfig()
  if (!await ensureServerPermission(cfg.server)) {
    return {
      ok: false,
      message: '请先在快捷面板保存鱼助手服务器和店铺，再打开官方扫码登录'
    }
  }

  let tab = null
  const saved = await chrome.storage.local.get({ interactiveLoginTabId: 0 })
  const existingTabId = Number(saved.interactiveLoginTabId || 0)
  if (existingTabId) {
    try {
      const existing = await chrome.tabs.get(existingTabId)
      if (existing && /^https:\/\/(?:www\.)?goofish\.com\/login(?:[?#]|$)/i.test(existing.url || '')) {
        tab = existing
      }
    } catch (error) {
      // 标签页已关闭时直接新建。
    }
  }
  if (!tab || !tab.id) {
    tab = await chrome.tabs.create({ url: INTERACTIVE_LOGIN_URL, active: false })
  }

  await chrome.storage.local.set({ interactiveLoginTabId: Number(tab.id) || 0 })
  const armed = await armInteractiveLogin()
  if (!scheduleVerification && interactiveLoginTimer) {
    clearTimeout(interactiveLoginTimer)
    interactiveLoginTimer = null
  }
  const qr = await captureInteractiveQr(tab.id)
  if (qr.ok) {
    await setInteractiveLoginState('waiting', '二维码已显示，请用闲鱼 App 扫码并确认')
    return {
      ...armed,
      tabId: Number(tab.id) || 0,
      mode: 'inline',
      qr: qr.dataUrl,
      message: '二维码已显示，请用闲鱼 App 扫码并确认'
    }
  }
  try {
    await chrome.tabs.update(tab.id, { active: true })
  } catch (error) {
    // 官方登录页已创建但激活失败时仍把具体原因交给快捷面板显示。
  }
  return {
    ...armed,
    tabId: Number(tab.id) || 0,
    mode: 'tab',
    qr: '',
    message: (qr.message || '未能读取二维码') + '，请直接在官方登录页扫码'
  }
}

async function refreshInteractiveQr() {
  const saved = await chrome.storage.local.get({ interactiveLoginTabId: 0 })
  const tabId = Number(saved.interactiveLoginTabId || 0)
  if (!tabId) return { ok: false, message: '官方登录页已关闭，请重新打开扫码登录' }
  try {
    await chrome.tabs.get(tabId)
  } catch (error) {
    return { ok: false, message: '官方登录页已关闭，请重新打开扫码登录' }
  }
  const qr = await captureInteractiveQr(tabId)
  if (!qr.ok) return { ok: false, message: qr.message }
  await setInteractiveLoginState('waiting', '二维码已刷新，请用闲鱼 App 扫码并确认')
  return { ok: true, qr: qr.dataUrl, message: '二维码已刷新' }
}

async function openInteractiveLoginTab() {
  const saved = await chrome.storage.local.get({ interactiveLoginTabId: 0 })
  const tabId = Number(saved.interactiveLoginTabId || 0)
  if (!tabId) {
    const result = await openInteractiveLogin(false)
    const openedTabId = Number(result && result.tabId) || 0
    if (openedTabId) {
      try { await chrome.tabs.update(openedTabId, { active: true }) } catch (error) {}
    }
    return { ...result, ok: true, mode: 'tab', message: '已打开闲鱼官方登录页' }
  }
  try {
    await chrome.tabs.update(tabId, { active: true })
    return { ok: true, mode: 'tab', tabId, message: '已切换到官方登录页' }
  } catch (error) {
    return openInteractiveLogin(false)
  }
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
        await completeInteractiveLogin(false, '还没有检测到闲鱼登录，请完成扫码后重试')
        return { ok: false, heartbeatOK, message: '请先在当前浏览器登录闲鱼' }
      }

      const fp = await fingerprint(jar.goofish, jar.sso)
      const stored = await chrome.storage.local.get({
        lastUploadedFingerprint: '',
        lastAttemptedFingerprint: '',
        lastAttemptAt: 0,
        lastResult: '',
        lastNick: '',
        lastLoginRefreshAt: 0,
        autoRefreshLogin: true
      })
      let currentJar = jar
      let currentFingerprint = fp
      let refreshNote = ''
      const unhealthy = needsBinding(status)
      const refreshInterval = unhealthy ? LOGIN_REFRESH_RETRY_MS : LOGIN_REFRESH_INTERVAL_MS
      const lastRefreshAt = Date.parse(stored.lastLoginRefreshAt || '') || 0
      const refreshDue = stored.autoRefreshLogin !== false &&
        (force || Date.now() - lastRefreshAt >= refreshInterval)
      if (refreshDue) {
        const refreshed = await refreshLoginSession(unhealthy ? 'recovery' : 'scheduled')
        refreshNote = refreshed.message
        if (refreshed.jar) {
          currentJar = refreshed.jar
          currentFingerprint = refreshed.fingerprint || currentFingerprint
        }
      }

      // “立即同步”只用于重新检查；浏览器凭证没有变化且服务端仍正常时，
      // 不应再次把同一份 Cookie 提交给闲鱼校验。重复校验可能被风控临时拒绝，
      // 从而被误显示成“Cookie 已失效”，但服务端原有凭证其实仍然可用。
      if (currentFingerprint === stored.lastUploadedFingerprint && !unhealthy) {
        const detail = needsBinding(status)
          ? '浏览器登录凭证已上报，但服务端尚未恢复；请检查服务端提示'
          : (refreshNote || '订单接口可用，登录态未变化')
        await setBadge(true, detail)
        await completeInteractiveLogin(true, detail, stored.lastNick || '')
        return { ok: true, heartbeatOK, message: detail, refreshed: !!refreshNote }
      }
      const coolingDown = currentFingerprint === stored.lastAttemptedFingerprint &&
        Date.now() - Number(stored.lastAttemptAt || 0) < RETRY_COOLDOWN_MS
      if (!force && coolingDown) {
        const minutes = Math.max(1, Math.ceil(
          (RETRY_COOLDOWN_MS - (Date.now() - Number(stored.lastAttemptAt || 0))) / 60000
        ))
        const message = '本次登录态校验失败，' + minutes + ' 分钟后再试；若已重新登录请在快捷面板点“立即同步”'
        await setBadge(false, message)
        await completeInteractiveLogin(false, message)
        return { ok: false, heartbeatOK, message }
      }
      await chrome.storage.local.set({
        lastAttemptedFingerprint: currentFingerprint,
        lastAttemptAt: Date.now()
      })
      const result = await requestJSON('/api/goofish/rebind', {
        method: 'POST',
        body: JSON.stringify({
          goofish: currentJar.goofishRaw,
          taobao: currentJar.ssoRaw,
          umid_token: ''
        })
      })
      await chrome.storage.local.set({
        lastUploadedFingerprint: currentFingerprint,
        lastResult: 'ok',
        lastSyncAt: new Date().toISOString(),
        lastNick: result.nick || ''
      })
      const message = refreshNote ? refreshNote + '，已自动上报并通过校验' : '已自动上报并通过校验'
      await setBadge(true, message)
      await completeInteractiveLogin(true, message, result.nick || '')
      return { ok: true, heartbeatOK, message, nick: result.nick || '', refreshed: !!refreshNote }
    } catch (error) {
      const message = error && error.message ? error.message : String(error)
      await chrome.storage.local.set({ lastResult: 'error', lastError: message, lastSyncAt: new Date().toISOString() })
      await setBadge(false, message)
      await completeInteractiveLogin(false, message)
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
  await cleanupOrphanRefreshTab()
  const existing = await chrome.alarms.get(ALARM_NAME)
  if (!existing) await chrome.alarms.create(ALARM_NAME, { periodInMinutes: 1 })
  const updateAlarm = await chrome.alarms.get(UPDATE_ALARM_NAME)
  if (!updateAlarm) await chrome.alarms.create(UPDATE_ALARM_NAME, { periodInMinutes: 24 * 60 })
  await checkExtensionUpdate(false)
  await syncNow(false)
}

chrome.runtime.onInstalled.addListener(async () => {
  await setup()
})

chrome.runtime.onStartup.addListener(setup)

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === ALARM_NAME) {
    // syncNow 负责上报/续期，它的角标只反映「本次是否成功提交 Cookie」；
    // 完成后再用实时连接状态校正角标，使其与快捷面板的「当前可用」一致，
    // 避免凭证仍有效却因冷却期/重复校验显示成 ! 或空白。
    syncNow(false).finally(() => { checkConnection().catch(() => {}) })
  }
  if (alarm.name === UPDATE_ALARM_NAME) checkExtensionUpdate(true)
})

chrome.cookies.onChanged.addListener((change) => {
  if (change.removed || !AUTH_COOKIE_NAMES.has(change.cookie.name)) return
  noteInteractiveLoginCookieChange().catch(() => {})
  debounceSync()
})

chrome.storage.onChanged.addListener((changes, area) => {
  if (area === 'local' && (changes.server || changes.shopId)) {
    debounceSync(300)
    checkExtensionUpdate(true)
  }
})

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message && message.type === 'sync-now') {
    syncNow(true).then(sendResponse)
    return true
  }
  if (message && message.type === 'get-runtime-status') {
    Promise.all([
      chrome.storage.local.get({
        lastSyncAt: '', lastResult: '', lastError: '', lastNick: '',
        lastLoginRefreshAt: '', lastLoginRefreshResult: '', lastLoginRefreshMessage: '',
        lastExtensionUpdateCheckAt: '', extensionLatestVersion: '',
        extensionCheckedVersion: '',
        extensionUpdateAvailable: false, extensionDownloadUrl: '',
        extensionLatestSha256: '', extensionUpdateError: '',
        interactiveLoginStartedAt: 0, interactiveLoginState: '',
        interactiveLoginMessage: '', interactiveLoginNick: '',
        interactiveLoginUpdatedAt: '', interactiveLoginFinishedAt: 0,
        interactiveLoginQr: '', interactiveLoginQrCapturedAt: 0,
        interactiveLoginQrError: ''
      }),
      getConfig()
    ]).then(([saved, cfg]) => sendResponse({
      ok: true,
      saved,
      config: cfg,
      currentExtensionVersion: chrome.runtime.getManifest().version
    }))
    return true
  }
  if (message && message.type === 'check-extension-update') {
    checkExtensionUpdate(true).then(sendResponse)
    return true
  }
  if (message && message.type === 'check-connection') {
    checkConnection().then(sendResponse)
    return true
  }
  if (message && message.type === 'arm-interactive-login') {
    armInteractiveLogin().then(sendResponse).catch((error) => {
      sendResponse({ ok: false, message: error && error.message ? error.message : String(error) })
    })
    return true
  }
  if (message && message.type === 'open-interactive-login') {
    openInteractiveLogin().then(sendResponse).catch((error) => {
      sendResponse({ ok: false, message: error && error.message ? error.message : String(error) })
    })
    return true
  }
  if (message && message.type === 'refresh-interactive-qr') {
    refreshInteractiveQr().then(sendResponse).catch((error) => {
      sendResponse({ ok: false, message: error && error.message ? error.message : String(error) })
    })
    return true
  }
  if (message && message.type === 'open-interactive-tab') {
    openInteractiveLoginTab().then(sendResponse).catch((error) => {
      sendResponse({ ok: false, message: error && error.message ? error.message : String(error) })
    })
    return true
  }
})

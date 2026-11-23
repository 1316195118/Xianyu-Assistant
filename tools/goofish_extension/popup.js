const DEFAULT_SERVER = 'http://10.10.10.251:18083'

const form = document.getElementById('settingsForm')
const serverInput = document.getElementById('server')
const shopInput = document.getElementById('shopId')
const tokenInput = document.getElementById('bridgeToken')
const autoRefreshInput = document.getElementById('autoRefreshLogin')
const tokenDetails = document.getElementById('tokenDetails')
const saveButton = document.getElementById('save')
const syncButton = document.getElementById('sync')
const message = document.getElementById('message')
const statusPanel = document.getElementById('statusPanel')
const statusBadge = document.getElementById('statusBadge')
const statusTitle = document.getElementById('statusTitle')
const statusDetail = document.getElementById('statusDetail')
const serverHealth = document.getElementById('serverHealth')
const bridgeHealth = document.getElementById('bridgeHealth')
const goofishHealth = document.getElementById('goofishHealth')
const officialLoginButton = document.getElementById('officialLogin')
const officialLoginLabel = document.getElementById('officialLoginLabel')
const interactiveLoginStatus = document.getElementById('interactiveLoginStatus')
const updateBanner = document.getElementById('updateBanner')
const updateTitle = document.getElementById('updateTitle')
const updateHint = document.getElementById('updateHint')
const openOptionsButton = document.getElementById('openOptions')
const openOptionsLabel = document.getElementById('openOptionsLabel')
const version = document.getElementById('version')
let openingOptions = false
let openingOfficialLogin = false

function normalizeServer(value) {
  return String(value || '').trim().replace(/\/+$/, '')
}

function permissionOrigin(server) {
  return new URL(server).origin + '/*'
}

function formatTime(value) {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString('zh-CN', {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit'
  })
}

function setMessage(text = '', kind = '') {
  message.textContent = text
  message.className = 'message' + (kind ? ' ' + kind : '')
}

function setBusy(button, busy) {
  button.disabled = busy
  button.classList.toggle('loading', busy)
}

function setStatus(kind, title, detail) {
  statusPanel.className = 'status-panel' + (kind ? ' ' + kind : '')
  statusBadge.className = 'status-badge ' + (kind || 'neutral')
  statusBadge.textContent = kind === 'ok'
    ? '当前可用'
    : kind === 'warn'
      ? '部分可用'
      : kind === 'error'
        ? '不可用'
        : '待连接'
  statusTitle.textContent = title
  statusDetail.textContent = detail
}

function setHealth(element, kind, label, value) {
  element.className = 'health-item ' + kind
  element.querySelector('b').textContent = label
  element.querySelector('small').textContent = value
}

function setCheckingHealth() {
  setHealth(serverHealth, 'checking', '服务器', '检测中')
  setHealth(bridgeHealth, 'checking', '配对码', '检测中')
  setHealth(goofishHealth, 'checking', '闲鱼', '检测中')
}

function renderConnection(result) {
  if (!result) {
    setHealth(serverHealth, 'error', '服务器', '无响应')
    setHealth(bridgeHealth, 'error', '配对码', '未检测')
    setHealth(goofishHealth, 'error', '闲鱼', '未检测')
    setStatus('error', '状态检查失败', '扩展后台没有返回连接状态')
    return
  }

  const status = result.status || {}
  setHealth(serverHealth, result.reachable ? 'ok' : 'error', '服务器', result.reachable ? '已连接' : '连接失败')
  setHealth(bridgeHealth, result.bridgeOK ? 'ok' : 'error', '配对码', result.bridgeOK ? '已授权' : '需检查')
  let goofishKind = 'error'
  let goofishText = '未绑定'
  if (status.order_api_valid) {
    goofishKind = 'ok'
    goofishText = '可用'
  } else if (status.session_valid) {
    goofishKind = 'warn'
    goofishText = '待验证'
  } else if (status.logged_in) {
    goofishText = '已失效'
  }
  setHealth(goofishHealth, goofishKind, '闲鱼', goofishText)

  if (result.usable) {
    const account = status.nick ? '「' + status.nick + '」' : ''
    setStatus('ok', '当前可用', '服务器、配对码和闲鱼订单接口均正常' + (account ? ' · ' + account : ''))
  } else if (result.reachable) {
    setStatus('warn', '当前部分可用', result.message || '服务器已连接，但还有一项状态需要处理')
  } else {
    setStatus('error', '当前不可用', result.message || '无法连接鱼助手服务器')
  }
}

function renderRuntime(runtime) {
  if (!runtime || !runtime.saved) return
  const saved = runtime.saved
  if (runtime.currentExtensionVersion) {
    version.textContent = '扩展 v' + runtime.currentExtensionVersion
  }
  renderInteractiveLogin(saved)
  if (saved.extensionUpdateAvailable) {
    updateBanner.hidden = false
    updateTitle.textContent = '发现扩展新版 v' + saved.extensionLatestVersion
  } else {
    updateBanner.hidden = true
  }

  if (saved.lastResult === 'ok' && saved.lastSyncAt) {
    const who = saved.lastNick ? saved.lastNick + ' · ' : ''
    const refreshed = saved.lastLoginRefreshAt
      ? ' · 续期 ' + formatTime(saved.lastLoginRefreshAt)
      : ''
    setStatus('', '正在检查实时状态', who + '上次同步 ' + formatTime(saved.lastSyncAt) + refreshed)
    return
  }
  if (saved.lastError) {
    setStatus('warn', '正在检查实时状态', saved.lastError)
    return
  }
  if (saved.lastLoginRefreshMessage) {
    const failed = saved.lastLoginRefreshResult === 'error'
    setStatus(failed ? 'warn' : '', failed ? '正在检查实时状态' : '等待首次同步', saved.lastLoginRefreshMessage)
    return
  }
  setStatus('', '等待连接', '保存服务器和店铺后，扩展会自动检查闲鱼登录态。')
}

function renderInteractiveLogin(saved) {
  const state = saved && saved.interactiveLoginState
  if (!state || !saved.interactiveLoginMessage) {
    interactiveLoginStatus.hidden = true
    return
  }
  interactiveLoginStatus.hidden = false
  interactiveLoginStatus.className = 'interactive-login-status ' + state
  interactiveLoginStatus.textContent = saved.interactiveLoginMessage
  if (state === 'success' && saved.interactiveLoginNick) {
    interactiveLoginStatus.textContent = '登录成功：' + saved.interactiveLoginNick + '，登录态已上报并通过校验'
  }
}

let connectionRequest = null
async function refreshConnection() {
  if (connectionRequest) return connectionRequest
  setCheckingHealth()
  statusBadge.className = 'status-badge neutral'
  statusBadge.textContent = '检测中'
  statusTitle.textContent = '正在检查连接状态'
  statusDetail.textContent = '正在确认服务器、配对码和闲鱼登录态…'
  connectionRequest = (async () => {
    try {
      const result = await chrome.runtime.sendMessage({ type: 'check-connection' })
      renderConnection(result)
    } catch (error) {
      renderConnection({ message: error.message || String(error) })
    }
  })()
  try {
    await connectionRequest
  } finally {
    connectionRequest = null
  }
}

async function load() {
  const cfg = await chrome.storage.local.get({
    server: DEFAULT_SERVER,
    shopId: 1,
    bridgeToken: '',
    autoRefreshLogin: true
  })
  serverInput.value = cfg.server || DEFAULT_SERVER
  shopInput.value = cfg.shopId || 1
  tokenInput.value = cfg.bridgeToken || ''
  autoRefreshInput.checked = cfg.autoRefreshLogin !== false
  if (cfg.bridgeToken) tokenDetails.open = true

  const runtime = await chrome.runtime.sendMessage({ type: 'get-runtime-status' })
  renderRuntime(runtime)
  await refreshConnection()
}

function readForm() {
  const server = normalizeServer(serverInput.value)
  const shopId = Number(shopInput.value)
  const bridgeToken = tokenInput.value.trim()
  const autoRefreshLogin = autoRefreshInput.checked
  const parsed = new URL(server)
  if (!['http:', 'https:'].includes(parsed.protocol)) {
    throw new Error('服务器地址必须以 http:// 或 https:// 开头')
  }
  if (!Number.isInteger(shopId) || shopId <= 0) {
    throw new Error('店铺 ID 必须是正整数')
  }
  return { server, shopId, bridgeToken, autoRefreshLogin }
}

async function save(event) {
  event.preventDefault()
  let cfg
  try {
    cfg = readForm()
  } catch (error) {
    setMessage(error.message || '配置格式不正确', 'error')
    return
  }

  setBusy(saveButton, true)
  setMessage('正在请求服务器访问权限…')
  try {
    const granted = await chrome.permissions.request({ origins: [permissionOrigin(cfg.server)] })
    if (!granted) {
      setMessage('未授权访问该服务器，扩展无法自动同步', 'error')
      return
    }
    await chrome.storage.local.set(cfg)
    setMessage(cfg.autoRefreshLogin ? '已保存，正在续期并检查登录态…' : '已保存，正在检查登录态…')
    const result = await chrome.runtime.sendMessage({ type: 'sync-now' })
    const text = result && result.message ? result.message : '检查完成'
    setMessage(text, result && result.ok ? 'ok' : 'error')
    await refreshConnection()
  } catch (error) {
    const text = error.message || String(error)
    setMessage(text, 'error')
    setStatus('error', '连接需要处理', text)
  } finally {
    setBusy(saveButton, false)
  }
}

async function syncNow() {
  setBusy(syncButton, true)
  setMessage(autoRefreshInput.checked ? '正在后台续期并校验登录态…' : '正在校验当前登录态…')
  try {
    const result = await chrome.runtime.sendMessage({ type: 'sync-now' })
    const text = result && result.message ? result.message : '检查完成'
    setMessage(text, result && result.ok ? 'ok' : 'error')
    await refreshConnection()
  } catch (error) {
    const text = error.message || String(error)
    setMessage(text, 'error')
    setStatus('error', '同步失败', text)
  } finally {
    setBusy(syncButton, false)
  }
}

async function openOfficialLogin() {
  if (openingOfficialLogin) return
  openingOfficialLogin = true
  officialLoginButton.disabled = true
  officialLoginButton.setAttribute('aria-disabled', 'true')
  officialLoginButton.classList.add('loading')
  officialLoginLabel.textContent = '正在打开官方登录页…'
  interactiveLoginStatus.hidden = false
  interactiveLoginStatus.className = 'interactive-login-status waiting'
  interactiveLoginStatus.textContent = '正在当前浏览器打开闲鱼官方登录页…'
  try {
    const result = await chrome.runtime.sendMessage({ type: 'arm-interactive-login' })
    if (!result || !result.ok) throw new Error(result && result.message ? result.message : '无法打开官方登录页')
    interactiveLoginStatus.textContent = result.message || '请在官方页面完成扫码'
    setMessage(result.message || '请在官方页面完成扫码', 'ok')
  } catch (error) {
    const text = error && error.message ? error.message : String(error)
    interactiveLoginStatus.className = 'interactive-login-status failed'
    interactiveLoginStatus.textContent = text
    setMessage(text, 'error')
  } finally {
    openingOfficialLogin = false
    officialLoginButton.disabled = false
    officialLoginButton.setAttribute('aria-disabled', 'false')
    officialLoginButton.classList.remove('loading')
    officialLoginLabel.textContent = '打开官方扫码登录'
  }
}

async function openOptionsPage() {
  if (openingOptions) return
  openingOptions = true
  updateBanner.classList.remove('error')
  updateBanner.classList.add('opening')
  updateBanner.setAttribute('aria-busy', 'true')
  updateHint.textContent = '正在打开高级设置…'
  openOptionsButton.disabled = true
  openOptionsLabel.textContent = '正在打开…'

  try {
    await GoofishExtensionNavigation.openOptionsPage(chrome)
    updateBanner.classList.remove('opening')
    updateBanner.classList.add('opened')
    updateHint.textContent = '高级设置已打开，可下载新版并重新加载'
    openOptionsLabel.textContent = '已打开'
    setTimeout(() => window.close(), 220)
  } catch (error) {
    const text = error && error.message ? error.message : String(error)
    updateBanner.classList.remove('opening', 'opened')
    updateBanner.classList.add('error')
    updateHint.textContent = text + '。请再点一次重试'
    openOptionsLabel.textContent = '重试高级设置'
  } finally {
    updateBanner.removeAttribute('aria-busy')
    openingOptions = false
    openOptionsButton.disabled = false
  }
}

form.addEventListener('submit', save)
syncButton.addEventListener('click', syncNow)
officialLoginButton.addEventListener('click', openOfficialLogin)
openOptionsButton.addEventListener('click', openOptionsPage)
updateBanner.addEventListener('click', (event) => {
  event.preventDefault()
  openOptionsPage()
})

load().catch((error) => {
  const text = error.message || String(error)
  setMessage(text, 'error')
  setStatus('error', '读取配置失败', text)
})

setInterval(refreshConnection, 30000)

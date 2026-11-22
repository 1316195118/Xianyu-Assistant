const DEFAULT_SERVER = 'http://10.10.10.251:18083'
const serverInput = document.getElementById('server')
const shopInput = document.getElementById('shopId')
const tokenInput = document.getElementById('bridgeToken')
const autoRefreshInput = document.getElementById('autoRefreshLogin')
const message = document.getElementById('message')
const updateBox = document.getElementById('updateBox')
const updateTitle = document.getElementById('updateTitle')
const updateDetail = document.getElementById('updateDetail')
const updateDownload = document.getElementById('updateDownload')
const checkUpdateButton = document.getElementById('checkUpdate')

function setMessage(text, kind = '') {
  message.textContent = text
  message.className = 'message' + (kind ? ' ' + kind : '')
}

function normalizeServer(value) {
  return String(value || '').trim().replace(/\/+$/, '')
}

function permissionOrigin(server) {
  return new URL(server).origin + '/*'
}

function formatTime(value) {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString()
}

function renderUpdate(state, currentVersion = '') {
  if (!state) return
  const current = state.currentExtensionVersion || currentVersion || ''
  const latest = state.extensionLatestVersion || ''
  const available = !!state.extensionUpdateAvailable
  const error = state.extensionUpdateError || ''
  const checkedAt = formatTime(state.lastExtensionUpdateCheckAt)
  updateBox.classList.toggle('available', available)
  updateBox.classList.toggle('error', !!error)
  updateDownload.hidden = !available || !state.extensionDownloadUrl
  if (available) {
    updateDownload.href = state.extensionDownloadUrl
    updateTitle.textContent = '发现扩展新版 v' + latest
    updateDetail.textContent = '当前 v' + current +
      '。下载后替换原解压目录，再到浏览器扩展页点一次“重新加载”；服务器、店铺和配对码会保留。'
  } else if (error) {
    updateTitle.textContent = '扩展更新检查失败'
    updateDetail.textContent = error
  } else {
    updateTitle.textContent = '扩展已是最新版本 v' + (current || latest)
    updateDetail.textContent = '每天自动检查一次' + (checkedAt ? ' · 上次检查 ' + checkedAt : '')
  }
}

async function requestUpdateCheck(force = true) {
  checkUpdateButton.disabled = true
  updateDetail.textContent = '正在检查鱼助手服务器…'
  try {
    const result = await chrome.runtime.sendMessage({ type: 'check-extension-update', force })
    const runtime = await chrome.runtime.sendMessage({ type: 'get-runtime-status' })
    const state = {
      ...(runtime && runtime.saved ? runtime.saved : {}),
      currentExtensionVersion: result && result.currentVersion,
      extensionLatestVersion: result && result.latestVersion,
      extensionUpdateAvailable: !!(result && result.updateAvailable),
      extensionDownloadUrl: result && result.downloadUrl,
      extensionUpdateError: result && !result.ok ? result.message : ''
    }
    renderUpdate(state)
    return result
  } catch (error) {
    renderUpdate({ extensionUpdateError: error.message || String(error) })
  } finally {
    checkUpdateButton.disabled = false
  }
}

async function load() {
  const cfg = await chrome.storage.local.get({
    server: DEFAULT_SERVER, shopId: 1, bridgeToken: '', autoRefreshLogin: true
  })
  serverInput.value = cfg.server || DEFAULT_SERVER
  shopInput.value = cfg.shopId || 1
  tokenInput.value = cfg.bridgeToken || ''
  autoRefreshInput.checked = cfg.autoRefreshLogin !== false
  const runtime = await chrome.runtime.sendMessage({ type: 'get-runtime-status' })
  if (runtime && runtime.saved) {
    renderUpdate({
      ...runtime.saved,
      currentExtensionVersion: runtime.currentExtensionVersion
    })
    if (runtime.saved.lastResult === 'ok' && runtime.saved.lastSyncAt) {
      let text = '上次同步成功：' + new Date(runtime.saved.lastSyncAt).toLocaleString()
      if (runtime.saved.lastLoginRefreshAt) {
        text += ' · 上次续期：' + new Date(runtime.saved.lastLoginRefreshAt).toLocaleString()
      }
      setMessage(text, 'ok')
    } else if (runtime.saved.lastError) {
      setMessage(runtime.saved.lastError, 'error')
    } else if (runtime.saved.lastLoginRefreshMessage) {
      setMessage(runtime.saved.lastLoginRefreshMessage, runtime.saved.lastLoginRefreshResult === 'error' ? 'error' : '')
    }
  }
  requestUpdateCheck(true)
}

async function save() {
  const server = normalizeServer(serverInput.value)
  const shopId = Number(shopInput.value)
  const bridgeToken = tokenInput.value.trim()
  const autoRefreshLogin = autoRefreshInput.checked
  try {
    new URL(server)
    if (!Number.isInteger(shopId) || shopId <= 0) throw new Error('店铺 ID 必须是正整数')
  } catch (error) {
    setMessage(error.message || '服务器地址无效', 'error')
    return
  }

  const granted = await chrome.permissions.request({ origins: [permissionOrigin(server)] })
  if (!granted) {
    setMessage('没有获得访问该服务器的权限，无法自动同步', 'error')
    return
  }
  await chrome.storage.local.set({ server, shopId, bridgeToken, autoRefreshLogin })
  setMessage(autoRefreshLogin ? '已保存，正在后台续期并检查登录态…' : '已保存，正在检查当前登录态…')
  const result = await chrome.runtime.sendMessage({ type: 'sync-now' })
  setMessage(result && result.message ? result.message : '检查完成', result && result.ok ? 'ok' : 'error')
  requestUpdateCheck(true)
}

async function syncNow() {
  setMessage(autoRefreshInput.checked ? '正在后台续期登录态并校验…' : '正在读取当前浏览器登录态并校验…')
  const result = await chrome.runtime.sendMessage({ type: 'sync-now' })
  setMessage(result && result.message ? result.message : '检查完成', result && result.ok ? 'ok' : 'error')
}

document.getElementById('save').addEventListener('click', save)
document.getElementById('sync').addEventListener('click', syncNow)
checkUpdateButton.addEventListener('click', () => requestUpdateCheck(true))
load().catch((error) => setMessage(error.message || String(error), 'error'))

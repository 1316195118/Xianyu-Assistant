const DEFAULT_SERVER = 'http://10.10.10.251:18083'
const serverInput = document.getElementById('server')
const shopInput = document.getElementById('shopId')
const tokenInput = document.getElementById('bridgeToken')
const autoRefreshInput = document.getElementById('autoRefreshLogin')
const message = document.getElementById('message')
const updateBox = document.getElementById('updateBox')
const updateTitle = document.getElementById('updateTitle')
const updateDetail = document.getElementById('updateDetail')
const updateProgress = document.getElementById('updateProgress')
const updateProgressBar = document.getElementById('updateProgressBar')
const updateProgressText = document.getElementById('updateProgressText')
const updateDownload = document.getElementById('updateDownload')
const updateDownloadLabel = document.getElementById('updateDownloadLabel')
const checkUpdateButton = document.getElementById('checkUpdate')
const checkUpdateLabel = document.getElementById('checkUpdateLabel')
const officialLoginButton = document.getElementById('officialLogin')
const officialLoginLabel = document.getElementById('officialLoginLabel')
const interactiveLoginStatus = document.getElementById('interactiveLoginStatus')
let updateBusy = false
let officialLoginBusy = false
let downloadUrl = ''
let latestVersion = ''
let interactiveLoginTimer = null

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

function setUpdateProgress(text, percent = null) {
  updateProgress.hidden = false
  updateProgress.classList.toggle('indeterminate', percent === null)
  updateProgressText.textContent = text
  updateProgressBar.style.width = percent === null ? '' : Math.max(0, Math.min(100, percent)) + '%'
}

function clearUpdateProgress() {
  updateProgress.hidden = true
  updateProgress.classList.remove('indeterminate')
  updateProgressBar.style.width = '0%'
  updateProgressText.textContent = ''
}

function setButtonBusy(button, label, busy, text) {
  button.disabled = busy
  button.setAttribute('aria-disabled', busy ? 'true' : 'false')
  button.classList.toggle('loading', busy)
  label.textContent = text
}

function renderInteractiveLogin(saved) {
  const state = saved && saved.interactiveLoginState
  if (!state || !saved.interactiveLoginMessage) {
    interactiveLoginStatus.hidden = true
    return
  }
  interactiveLoginStatus.hidden = false
  interactiveLoginStatus.className = 'interactive-login-status ' + state
  interactiveLoginStatus.textContent = saved.interactiveLoginNick && state === 'success'
    ? '登录成功：' + saved.interactiveLoginNick + '，登录态已上报并通过校验'
    : saved.interactiveLoginMessage
}

function scheduleInteractiveLoginRefresh(saved) {
  if (interactiveLoginTimer) clearInterval(interactiveLoginTimer)
  interactiveLoginTimer = null
  if (!saved || !['waiting', 'verifying'].includes(saved.interactiveLoginState)) return
  interactiveLoginTimer = setInterval(async () => {
    try {
      const runtime = await chrome.runtime.sendMessage({ type: 'get-runtime-status' })
      const latest = runtime && runtime.saved
      renderInteractiveLogin(latest)
      if (!latest || !['waiting', 'verifying'].includes(latest.interactiveLoginState)) {
        clearInterval(interactiveLoginTimer)
        interactiveLoginTimer = null
      }
    } catch (error) {
      // 单次状态读取失败不打断当前页面，下一次继续刷新。
    }
  }, 2500)
}

function renderUpdate(state, currentVersion = '') {
  if (!state) return
  const current = state.currentExtensionVersion || currentVersion || ''
  const latest = state.extensionLatestVersion || ''
  const available = !!state.extensionUpdateAvailable
  const error = state.extensionUpdateError || ''
  const checkedAt = formatTime(state.lastExtensionUpdateCheckAt)
  latestVersion = latest
  downloadUrl = available ? String(state.extensionDownloadUrl || '') : ''
  updateBox.classList.remove('checking', 'downloading', 'ready')
  updateBox.classList.toggle('available', available)
  updateBox.classList.toggle('error', !!error)
  updateDownload.hidden = !available || !downloadUrl
  updateDownload.classList.remove('loading')
  updateDownloadLabel.textContent = '下载新版'
  clearUpdateProgress()
  if (available) {
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
  if (updateBusy) return
  updateBusy = true
  updateBox.classList.remove('available', 'error', 'downloading', 'ready')
  updateBox.classList.add('checking')
  updateTitle.textContent = '正在检查扩展更新'
  updateDetail.textContent = '正在连接鱼助手服务器核对最新版本…'
  setUpdateProgress('检查中，请稍候…')
  setButtonBusy(checkUpdateButton, checkUpdateLabel, true, '检查中')
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
    updateBox.classList.remove('checking')
    setButtonBusy(checkUpdateButton, checkUpdateLabel, false, '检查更新')
    updateBusy = false
  }
}

async function readDownloadBlob(response) {
  const total = Number(response.headers.get('content-length')) || 0
  if (!response.body || typeof response.body.getReader !== 'function') {
    return response.blob()
  }

  const reader = response.body.getReader()
  const chunks = []
  let received = 0
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    chunks.push(value)
    received += value.byteLength
    const percent = total ? Math.min(99, Math.round(received / total * 100)) : null
    setUpdateProgress(percent === null ? '正在下载扩展包…' : '正在下载扩展包 ' + percent + '%', percent)
  }
  return new Blob(chunks, { type: 'application/zip' })
}

function saveBlob(blob) {
  const objectUrl = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = objectUrl
  link.download = 'goofish-extension-' + (latestVersion || 'latest') + '.zip'
  link.hidden = true
  document.body.append(link)
  link.click()
  link.remove()
  setTimeout(() => URL.revokeObjectURL(objectUrl), 10000)
}

async function downloadUpdate() {
  if (updateBusy || !downloadUrl) return
  updateBusy = true
  updateBox.classList.remove('error', 'ready')
  updateBox.classList.add('downloading', 'available')
  updateTitle.textContent = '正在下载扩展包 v' + latestVersion
  updateDetail.textContent = '下载完成后请解压覆盖原目录，并在浏览器扩展页重新加载。'
  setUpdateProgress('正在准备下载…', 0)
  setButtonBusy(updateDownload, updateDownloadLabel, true, '下载中')
  setButtonBusy(checkUpdateButton, checkUpdateLabel, true, '检查更新')

  try {
    const response = await fetch(downloadUrl, { cache: 'no-store', credentials: 'omit' })
    if (!response.ok) throw new Error('服务器返回 ' + response.status)
    const blob = await readDownloadBlob(response)
    if (!blob.size) throw new Error('下载文件为空')
    saveBlob(blob)
    setUpdateProgress('下载完成，已交给浏览器保存', 100)
    updateBox.classList.remove('downloading')
    updateBox.classList.add('ready')
    updateTitle.textContent = '扩展包下载完成 v' + latestVersion
    updateDetail.textContent = '解压下载的压缩包并覆盖原扩展目录，然后打开浏览器扩展页点“重新加载”。原有设置会保留。'
    setButtonBusy(updateDownload, updateDownloadLabel, false, '再次下载')
  } catch (error) {
    const text = error && error.message ? error.message : String(error)
    updateBox.classList.remove('downloading', 'ready')
    updateBox.classList.add('error')
    updateTitle.textContent = '扩展包下载失败'
    updateDetail.textContent = text + '。请确认鱼助手服务器可访问后重试。'
    setUpdateProgress('下载未完成', 0)
    setButtonBusy(updateDownload, updateDownloadLabel, false, '重试下载')
  } finally {
    setButtonBusy(checkUpdateButton, checkUpdateLabel, false, '检查更新')
    updateBusy = false
  }
}

async function openOfficialLogin() {
  if (officialLoginBusy) return
  officialLoginBusy = true
  setButtonBusy(officialLoginButton, officialLoginLabel, true, '正在打开官方登录页…')
  interactiveLoginStatus.hidden = false
  interactiveLoginStatus.className = 'interactive-login-status waiting'
  interactiveLoginStatus.textContent = '正在当前浏览器打开闲鱼官方登录页…'
  try {
    const result = await chrome.runtime.sendMessage({ type: 'open-interactive-tab' })
    if (!result || !result.ok) throw new Error(result && result.message ? result.message : '无法打开官方登录页')
    interactiveLoginStatus.textContent = result.message || '请在官方页面完成扫码'
    setMessage('已打开闲鱼官方登录页，请用闲鱼 App 扫码', 'ok')
    scheduleInteractiveLoginRefresh({ interactiveLoginState: 'waiting' })
  } catch (error) {
    const text = error && error.message ? error.message : String(error)
    interactiveLoginStatus.className = 'interactive-login-status failed'
    interactiveLoginStatus.textContent = text
    setMessage(text, 'error')
  } finally {
    officialLoginBusy = false
    setButtonBusy(officialLoginButton, officialLoginLabel, false, '打开闲鱼官方扫码登录')
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
    renderInteractiveLogin(runtime.saved)
    scheduleInteractiveLoginRefresh(runtime.saved)
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
updateDownload.addEventListener('click', downloadUpdate)
officialLoginButton.addEventListener('click', openOfficialLogin)
load().catch((error) => setMessage(error.message || String(error), 'error'))

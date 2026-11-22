const DEFAULT_SERVER = 'http://10.10.10.251:18083'
const serverInput = document.getElementById('server')
const shopInput = document.getElementById('shopId')
const tokenInput = document.getElementById('bridgeToken')
const message = document.getElementById('message')

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

async function load() {
  const cfg = await chrome.storage.local.get({ server: DEFAULT_SERVER, shopId: 1, bridgeToken: '' })
  serverInput.value = cfg.server || DEFAULT_SERVER
  shopInput.value = cfg.shopId || 1
  tokenInput.value = cfg.bridgeToken || ''
  const runtime = await chrome.runtime.sendMessage({ type: 'get-runtime-status' })
  if (runtime && runtime.saved) {
    if (runtime.saved.lastResult === 'ok' && runtime.saved.lastSyncAt) {
      setMessage('上次同步成功：' + new Date(runtime.saved.lastSyncAt).toLocaleString(), 'ok')
    } else if (runtime.saved.lastError) {
      setMessage(runtime.saved.lastError, 'error')
    }
  }
}

async function save() {
  const server = normalizeServer(serverInput.value)
  const shopId = Number(shopInput.value)
  const bridgeToken = tokenInput.value.trim()
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
  await chrome.storage.local.set({ server, shopId, bridgeToken })
  setMessage('已保存，正在检查当前登录态…')
  const result = await chrome.runtime.sendMessage({ type: 'sync-now' })
  setMessage(result && result.message ? result.message : '检查完成', result && result.ok ? 'ok' : 'error')
}

async function syncNow() {
  setMessage('正在读取当前浏览器登录态并校验…')
  const result = await chrome.runtime.sendMessage({ type: 'sync-now' })
  setMessage(result && result.message ? result.message : '检查完成', result && result.ok ? 'ok' : 'error')
}

document.getElementById('save').addEventListener('click', save)
document.getElementById('sync').addEventListener('click', syncNow)
load().catch((error) => setMessage(error.message || String(error), 'error'))

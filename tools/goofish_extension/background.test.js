const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')
const vm = require('node:vm')

const source = fs.readFileSync(path.join(__dirname, 'background.js'), 'utf8')

function makeBackground({ cookies, fetchImpl, config = {} }) {
  const store = {
    server: 'http://10.10.10.251:18083',
    shopId: 1,
    bridgeToken: '',
    autoRefreshLogin: true,
    ...config
  }
  const noop = () => {}
  const asyncNoop = async () => {}
  const tabUpdated = {
    listeners: new Set(),
    addListener(fn) { this.listeners.add(fn) },
    removeListener(fn) { this.listeners.delete(fn) },
    emit(...args) { for (const fn of this.listeners) fn(...args) }
  }
  let nextTabId = 100
  const tabs = []
  const removedTabs = []
  const badgeTexts = []
  const chrome = {
    storage: {
      local: {
        async get(defaults) { return { ...defaults, ...store } },
        async set(values) { Object.assign(store, values) }
      },
      onChanged: { addListener: noop }
    },
    cookies: {
      async getAll({ url }) {
        if (url.includes('goofish.com')) return cookies.goofish || []
        if (url.includes('taobao.com')) return cookies.taobao || []
        return cookies.tmall || []
      },
      onChanged: { addListener: noop }
    },
    permissions: { async contains() { return true } },
    alarms: {
      async create() {},
      async get() { return null },
      onAlarm: { addListener: noop }
    },
    action: {
      async setBadgeText({ text }) { badgeTexts.push(text) },
      async setBadgeBackgroundColor() {},
      async setTitle() {},
      onClicked: { addListener: noop }
    },
    tabs: {
      async create(options) {
        const tab = { id: nextTabId++, url: options.url, status: 'loading' }
        tabs.push(tab)
        setTimeout(() => tabUpdated.emit(tab.id, { status: 'complete' }, tab), 1)
        return tab
      },
      async get(tabId) {
        return tabs.find((tab) => tab.id === tabId) || { id: tabId, status: 'loading', url: 'https://www.goofish.com/' }
      },
      async remove(tabId) { removedTabs.push(tabId) },
      onUpdated: tabUpdated
    },
    runtime: {
      onInstalled: { addListener: noop },
      onStartup: { addListener: noop },
      onMessage: { addListener: noop },
      async openOptionsPage() {},
      getManifest() { return { version: '1.2.1' } }
    }
  }
  const context = vm.createContext({
    chrome,
    console,
    crypto: globalThis.crypto,
    TextEncoder,
    URL,
    fetch: fetchImpl,
    AbortSignal,
    setTimeout,
    clearTimeout
  })
  vm.runInContext(source, context)
  return { context, store, asyncNoop, tabs, removedTabs, badgeTexts }
}

test('上报成功后同一登录态不会重复提交，并携带扩展配对码', async () => {
  const calls = []
  const { context, store } = makeBackground({
    config: { bridgeToken: 'pair-token' },
    cookies: {
      goofish: [
        { name: 'cookie2', value: 'cookie-value' },
        { name: 'unb', value: 'user-1' }
      ]
    },
    fetchImpl: async (url, options) => {
      calls.push({ url, options })
      if (url.endsWith('/api/goofish/status')) {
        return { ok: true, status: 200, json: async () => ({ logged_in: false, credential_saved: false }) }
      }
      if (url.endsWith('/api/goofish/bridge/heartbeat')) {
        return { ok: true, status: 200, json: async () => ({ ok: true }) }
      }
      return { ok: true, status: 200, json: async () => ({ ok: true, nick: '测试账号' }) }
    }
  })

  const first = await context.syncNow(true)
  assert.equal(first.ok, true)
  const binds = calls.filter((call) => call.url.endsWith('/api/goofish/rebind'))
  assert.equal(binds.length, 1)
  assert.equal(binds[0].options.headers['X-Bridge-Token'], 'pair-token')
  assert.equal(binds[0].options.headers['X-Shop-Id'], '1')
  assert.ok(store.lastUploadedFingerprint)
  assert.equal(store.lastResult, 'ok')

  await context.syncNow(false)
  assert.equal(calls.filter((call) => call.url.endsWith('/api/goofish/rebind')).length, 1)
})

test('服务端正常且凭证未变化时，手动同步不会重复提交 Cookie', async () => {
  let rebindCalls = 0
  const { context } = makeBackground({
    cookies: {
      goofish: [
        { name: 'cookie2', value: 'valid-cookie' },
        { name: 'unb', value: 'user-1' }
      ]
    },
    fetchImpl: async (url) => {
      if (url.endsWith('/api/goofish/status')) {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            logged_in: true,
            session_valid: true,
            order_api_valid: true,
            expired: false,
            sync_error: ''
          })
        }
      }
      if (url.endsWith('/api/goofish/bridge/heartbeat')) {
        return { ok: true, status: 200, json: async () => ({ ok: true }) }
      }
      rebindCalls++
      return { ok: true, status: 200, json: async () => ({ ok: true, nick: '测试账号' }) }
    }
  })

  const first = await context.syncNow(true)
  assert.equal(first.ok, true)
  assert.equal(rebindCalls, 1)

  const second = await context.syncNow(true)
  assert.equal(second.ok, true)
  assert.match(second.message, /后台续期完成|登录态未变化/)
  assert.equal(rebindCalls, 1, '服务端正常且浏览器凭证未变化时，不应重复提交')
})

test('同一失败登录态进入冷却期，不会每分钟重复上报', async () => {
  let rebindCalls = 0
  const { context, tabs } = makeBackground({
    cookies: {
      goofish: [
        { name: 'cookie2', value: 'expired-value' },
        { name: 'unb', value: 'user-1' }
      ]
    },
    fetchImpl: async (url) => {
      if (url.endsWith('/api/goofish/status')) {
        return {
          ok: true,
          status: 200,
          json: async () => ({ logged_in: true, credential_saved: true, expired: true })
        }
      }
      if (url.endsWith('/api/goofish/bridge/heartbeat')) {
        return { ok: true, status: 200, json: async () => ({ ok: true }) }
      }
      rebindCalls++
      return { ok: false, status: 400, json: async () => ({ error: '登录态校验失败' }) }
    }
  })

  const forced = await context.syncNow(true)
  assert.equal(forced.ok, false)
  assert.equal(rebindCalls, 1)
  const automatic = await context.syncNow(false)
  assert.equal(automatic.ok, false)
  assert.match(automatic.message, /分钟后再试/)
  assert.equal(rebindCalls, 1)
  assert.equal(tabs.length, 1, '手动强制续期后，冷却期内不应再打开后台页面')
})

test('服务器启用访问密码且未配置配对码时给出明确提示', async () => {
  const { context } = makeBackground({
    cookies: {
      goofish: [
        { name: 'cookie2', value: 'cookie-value' },
        { name: 'unb', value: 'user-1' }
      ]
    },
    fetchImpl: async () => ({
      ok: false,
      status: 401,
      json: async () => ({ error: '未登录' })
    })
  })
  const result = await context.syncNow(true)
  assert.equal(result.ok, false)
  assert.match(result.message, /配对码/)
})

test('服务端登录失效时后台打开闲鱼页，拿到新 Cookie 后自动上报并关闭临时标签', async () => {
  const cookies = {
    goofish: [
      { name: 'cookie2', value: 'old-cookie' },
      { name: 'unb', value: 'user-1' }
    ]
  }
  let rebindBody = null
  const { context, store, tabs, removedTabs } = makeBackground({
    cookies,
    fetchImpl: async (url, options) => {
      if (url.endsWith('/api/goofish/status')) {
        return {
          ok: true,
          status: 200,
          json: async () => ({ logged_in: true, credential_saved: true, expired: true, sync_error: '登录态已过期' })
        }
      }
      if (url.endsWith('/api/goofish/bridge/heartbeat')) {
        return { ok: true, status: 200, json: async () => ({ ok: true }) }
      }
      rebindBody = JSON.parse(options.body)
      return { ok: true, status: 200, json: async () => ({ ok: true, nick: '测试账号' }) }
    }
  })
  tabs.length = 0
  // 模拟闲鱼首页加载期间刷新了会话 Cookie。
  const originalCreate = context.chrome.tabs.create
  context.chrome.tabs.create = async (options) => {
    cookies.goofish = [
      { name: 'cookie2', value: 'new-cookie' },
      { name: 'unb', value: 'user-1' }
    ]
    return originalCreate(options)
  }

  const result = await context.syncNow(false)
  assert.equal(result.ok, true)
  assert.equal(result.refreshed, true)
  assert.match(rebindBody.goofish, /cookie2=new-cookie/)
  assert.equal(tabs.length, 1)
  assert.deepEqual(removedTabs, [tabs[0].id])
  assert.equal(store.lastLoginRefreshResult, 'ok')
  assert.equal(store.lastResult, 'ok')
})

test('登录有效时定时后台续期但 Cookie 未变化，不会重复上传', async () => {
  let rebindCalls = 0
  const { context, store, tabs } = makeBackground({
    config: { autoRefreshLogin: false },
    cookies: {
      goofish: [
        { name: 'cookie2', value: 'valid-cookie' },
        { name: 'unb', value: 'user-1' }
      ]
    },
    fetchImpl: async (url) => {
      if (url.endsWith('/api/goofish/status')) {
        return {
          ok: true,
          status: 200,
          json: async () => ({ logged_in: true, credential_saved: true, expired: false, order_api_valid: true })
        }
      }
      if (url.endsWith('/api/goofish/bridge/heartbeat')) {
        return { ok: true, status: 200, json: async () => ({ ok: true }) }
      }
      rebindCalls++
      return { ok: true, status: 200, json: async () => ({ ok: true, nick: '测试账号' }) }
    }
  })
  await context.syncNow(true)
  assert.equal(rebindCalls, 1)

  store.autoRefreshLogin = true
  store.lastLoginRefreshAt = new Date(0).toISOString()
  const result = await context.syncNow(false)
  assert.equal(result.ok, true)
  assert.equal(result.refreshed, true)
  assert.equal(rebindCalls, 1)
  assert.equal(tabs.length, 1)
})

test('异常恢复只关闭扩展专用临时页，不误关用户的闲鱼标签', async () => {
  const { context, store, tabs, removedTabs } = makeBackground({
    cookies: { goofish: [{ name: 'cookie2', value: 'cookie-value' }] },
    fetchImpl: async () => ({ ok: true, status: 200, json: async () => ({}) })
  })
  tabs.push({ id: 201, url: 'https://www.goofish.com/', status: 'complete' })
  store.loginRefreshTabId = 201
  await context.cleanupOrphanRefreshTab()
  assert.deepEqual(removedTabs, [])

  tabs.push({ id: 202, url: 'https://www.goofish.com/?xy_ext_refresh=1', status: 'complete' })
  store.loginRefreshTabId = 202
  await context.cleanupOrphanRefreshTab()
  assert.deepEqual(removedTabs, [202])
})

test('从鱼助手服务器检查扩展新版并设置更新角标', async () => {
  const { context, store, badgeTexts } = makeBackground({
    cookies: { goofish: [] },
    fetchImpl: async (url) => {
      assert.match(url, /\/downloads\/goofish-extension-version\.json$/)
      return {
        ok: true,
        status: 200,
        json: async () => ({
          version: '1.3.0',
          download_path: '/downloads/goofish-extension.zip',
          sha256: 'abc123'
        })
      }
    }
  })
  const result = await context.checkExtensionUpdate(true)
  assert.equal(result.ok, true)
  assert.equal(result.currentVersion, '1.2.1')
  assert.equal(result.latestVersion, '1.3.0')
  assert.equal(result.updateAvailable, true)
  assert.equal(result.downloadUrl, 'http://10.10.10.251:18083/downloads/goofish-extension.zip')
  assert.equal(store.extensionUpdateAvailable, true)
  assert.equal(store.extensionLatestVersion, '1.3.0')
  assert.equal(badgeTexts.at(-1), '↑')
})

test('扩展已是最新版本时不显示更新下载入口', async () => {
  const { context, store } = makeBackground({
    cookies: { goofish: [] },
    fetchImpl: async () => ({
      ok: true,
      status: 200,
      json: async () => ({
        version: '1.2.0',
        download_path: '/downloads/goofish-extension.zip'
      })
    })
  })
  const result = await context.checkExtensionUpdate(true)
  assert.equal(result.ok, true)
  assert.equal(result.updateAvailable, false)
  assert.equal(store.extensionUpdateAvailable, false)
})

test('扩展版本改变后会忽略当天旧缓存，重新检查服务器', async () => {
  let checks = 0
  const { context, store } = makeBackground({
    config: {
      lastExtensionUpdateCheckAt: new Date().toISOString(),
      extensionCheckedVersion: '1.0.0',
      extensionLatestVersion: '1.2.0',
      extensionUpdateAvailable: true
    },
    cookies: { goofish: [] },
    fetchImpl: async () => {
      checks++
      return {
        ok: true,
        status: 200,
        json: async () => ({ version: '1.1.0' })
      }
    }
  })
  const result = await context.checkExtensionUpdate(false)
  assert.equal(checks, 1)
  assert.equal(result.updateAvailable, false)
  assert.equal(store.extensionCheckedVersion, '1.2.1')
})

test('版本号按数字段比较，不会把 1.1.10 误判低于 1.1.9', () => {
  const { context } = makeBackground({
    cookies: { goofish: [] },
    fetchImpl: async () => ({ ok: true, status: 200, json: async () => ({}) })
  })
  assert.equal(context.compareVersions('1.1.10', '1.1.9'), 1)
  assert.equal(context.compareVersions('1.2.0', '1.10.0'), -1)
  assert.equal(context.compareVersions('1.1', '1.1.0'), 0)
})

test('连接检查会同时验证服务器、配对码和闲鱼订单接口', async () => {
  const calls = []
  const { context } = makeBackground({
    cookies: { goofish: [] },
    config: { bridgeToken: 'pair-token' },
    fetchImpl: async (url) => {
      calls.push(url)
      if (url.endsWith('/api/goofish/status')) {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            shop_id: 1,
            logged_in: true,
            session_valid: true,
            order_api_valid: true,
            expired: false,
            sync_error: '',
            nick: '测试账号'
          })
        }
      }
      return { ok: true, status: 200, json: async () => ({ ok: true }) }
    }
  })

  const result = await context.checkConnection()
  assert.equal(result.usable, true)
  assert.equal(result.reachable, true)
  assert.equal(result.bridgeOK, true)
  assert.equal(result.status.nick, '测试账号')
  assert.ok(calls.some((url) => url.endsWith('/api/goofish/status')))
  assert.ok(calls.some((url) => url.endsWith('/api/goofish/bridge/heartbeat')))
})

test('服务器可访问但闲鱼登录态失效时，连接状态为部分可用', async () => {
  const { context } = makeBackground({
    cookies: { goofish: [] },
    fetchImpl: async (url) => {
      if (url.endsWith('/api/goofish/status')) {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            logged_in: true,
            session_valid: false,
            order_api_valid: false,
            expired: true,
            sync_error: '登录态已过期'
          })
        }
      }
      return { ok: true, status: 200, json: async () => ({ ok: true }) }
    }
  })

  const result = await context.checkConnection()
  assert.equal(result.reachable, true)
  assert.equal(result.bridgeOK, true)
  assert.equal(result.usable, false)
  assert.match(result.message, /登录态已失效/)
})

test('配对码无效时区分服务器可达与鉴权失败', async () => {
  const { context } = makeBackground({
    cookies: { goofish: [] },
    config: { bridgeToken: 'invalid-token' },
    fetchImpl: async () => ({
      ok: false,
      status: 401,
      json: async () => ({ error: '未登录' })
    })
  })

  const result = await context.checkConnection()
  assert.equal(result.reachable, true)
  assert.equal(result.bridgeOK, false)
  assert.equal(result.usable, false)
  assert.match(result.message, /配对码无效/)
})

test('未授权服务器访问时连接检查给出快捷面板指引', async () => {
  const { context } = makeBackground({
    cookies: { goofish: [] },
    fetchImpl: async () => {
      throw new Error('不应发起网络请求')
    }
  })
  context.chrome.permissions.contains = async () => false

  const result = await context.checkConnection()
  assert.equal(result.reachable, false)
  assert.equal(result.usable, false)
  assert.match(result.message, /快捷面板/)
})

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
    ...config
  }
  const noop = () => {}
  const asyncNoop = async () => {}
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
      async setBadgeText() {},
      async setBadgeBackgroundColor() {},
      async setTitle() {},
      onClicked: { addListener: noop }
    },
    runtime: {
      onInstalled: { addListener: noop },
      onStartup: { addListener: noop },
      onMessage: { addListener: noop },
      async openOptionsPage() {}
    }
  }
  const context = vm.createContext({
    chrome,
    console,
    crypto: globalThis.crypto,
    TextEncoder,
    URL,
    fetch: fetchImpl,
    setTimeout,
    clearTimeout
  })
  vm.runInContext(source, context)
  return { context, store, asyncNoop }
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

test('同一失败登录态进入冷却期，不会每分钟重复上报', async () => {
  let rebindCalls = 0
  const { context } = makeBackground({
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

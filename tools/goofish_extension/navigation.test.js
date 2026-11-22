const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')
const vm = require('node:vm')

const source = fs.readFileSync(path.join(__dirname, 'navigation.js'), 'utf8')

function loadNavigation(chrome) {
  const context = vm.createContext({
    chrome,
    clearTimeout,
    Error,
    Promise,
    setTimeout
  })
  vm.runInContext(source, context)
  return context.GoofishExtensionNavigation
}

test('优先通过扩展 API 打开高级设置', async () => {
  let opened = 0
  let createdTab = false
  const navigation = loadNavigation({
    runtime: {
      async openOptionsPage() {
        opened++
      },
      getURL(file) {
        return 'chrome-extension://test/' + file
      }
    },
    tabs: {
      async create() {
        createdTab = true
      }
    }
  })

  await navigation.openOptionsPage({
    runtime: {
      async openOptionsPage() {
        opened++
      },
      getURL(file) {
        return 'chrome-extension://test/' + file
      }
    },
    tabs: {
      async create() {
        createdTab = true
      }
    }
  })
  assert.equal(opened, 1)
  assert.equal(createdTab, false)
})

test('扩展 API 失败时直接新建高级设置标签页', async () => {
  let createdUrl = ''
  const chrome = {
    runtime: {
      async openOptionsPage() {
        throw new Error('openOptionsPage unavailable')
      },
      getURL(file) {
        return 'chrome-extension://test/' + file
      }
    },
    tabs: {
      async create(options) {
        createdUrl = options.url
      }
    }
  }
  const navigation = loadNavigation(chrome)

  await navigation.openOptionsPage(chrome)
  assert.equal(createdUrl, 'chrome-extension://test/options.html')
})

test('两种打开方式都失败时返回可显示的错误', async () => {
  const chrome = {
    runtime: {
      async openOptionsPage() {
        throw new Error('settings blocked')
      },
      getURL(file) {
        return 'chrome-extension://test/' + file
      }
    },
    tabs: {
      async create() {
        throw new Error('tabs blocked')
      }
    }
  }
  const navigation = loadNavigation(chrome)

  await assert.rejects(
    navigation.openOptionsPage(chrome),
    /无法打开高级设置：tabs blocked/
  )
})

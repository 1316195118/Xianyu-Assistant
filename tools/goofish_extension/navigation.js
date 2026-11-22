(function (root) {
  'use strict'

  function callChrome(invoke, timeoutMs = 3000) {
    return new Promise((resolve, reject) => {
      let settled = false
      let timer = null

      const finish = (error) => {
        if (settled) return
        settled = true
        if (timer) clearTimeout(timer)
        if (error) reject(error)
        else resolve()
      }

      timer = setTimeout(() => {
        finish(new Error('浏览器响应超时'))
      }, timeoutMs)

      let pending
      try {
        pending = invoke(() => {
          const runtimeError = root.chrome && root.chrome.runtime && root.chrome.runtime.lastError
          finish(runtimeError ? new Error(runtimeError.message || String(runtimeError)) : null)
        })
      } catch (error) {
        finish(error instanceof Error ? error : new Error(String(error)))
        return
      }
      if (pending && typeof pending.then === 'function') {
        pending.then(() => finish(), (error) => finish(error instanceof Error ? error : new Error(String(error))))
      }
    })
  }

  async function openOptionsPage(chromeApi) {
    if (!chromeApi || !chromeApi.runtime || typeof chromeApi.runtime.openOptionsPage !== 'function') {
      throw new Error('当前浏览器不支持打开扩展设置页')
    }

    try {
      await callChrome((done) => chromeApi.runtime.openOptionsPage(done))
      return
    } catch (primaryError) {
      if (!chromeApi.tabs || typeof chromeApi.tabs.create !== 'function') {
        throw primaryError
      }

      try {
        const optionsUrl = chromeApi.runtime.getURL('options.html')
        await callChrome((done) => chromeApi.tabs.create({ url: optionsUrl, active: true }, done))
      } catch (fallbackError) {
        const primary = primaryError && primaryError.message ? primaryError.message : String(primaryError)
        const fallback = fallbackError && fallbackError.message ? fallbackError.message : String(fallbackError)
        throw new Error('无法打开高级设置：' + fallback + '（原方式：' + primary + '）')
      }
    }
  }

  root.GoofishExtensionNavigation = Object.freeze({ openOptionsPage })
})(globalThis)

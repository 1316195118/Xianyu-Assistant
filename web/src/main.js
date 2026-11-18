import { createApp } from 'vue'
import App from './App.vue'
import './style.css'
import './theme'

createApp(App).mount('#app')

// 部署新版后，浏览器里还开着的旧页面引用的分包文件名已被清理，
// 点进「明细 / 联单 / 设置」时动态 import 会 404，页面就停在那里出不来。
// Vite 对这种情况会派发 preloadError，这里整页重载一次即可自愈；
// 用 sessionStorage 做 60 秒限流，避免网络异常时反复刷新成死循环。
window.addEventListener('vite:preloadError', (e) => {
  if (e && e.preventDefault) e.preventDefault()
  try {
    const KEY = 'xy-preload-reload-at'
    const last = Number(sessionStorage.getItem(KEY) || 0)
    if (last && Date.now() - last < 60000) return
    sessionStorage.setItem(KEY, String(Date.now()))
  } catch (err) { /* 隐私模式：不做限流，直接重载一次 */ }
  location.reload()
})

// Service Worker：离线兜底 + 新版本提示（失败静默降级）
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').then((reg) => {
      navigator.serviceWorker.addEventListener('message', (e) => {
        if (e.data && e.data.type === 'UPDATE_AVAILABLE') {
          window.dispatchEvent(new CustomEvent('xy-update', { detail: e.data }))
        }
      })
      reg.addEventListener('updatefound', () => {
        const w = reg.installing
        w && w.addEventListener('statechange', () => {
          if (w.state === 'installed' && navigator.serviceWorker.controller) {
            window.dispatchEvent(new CustomEvent('xy-update', { detail: { build: 'new' } }))
          }
        })
      })
      setTimeout(() => reg.active && reg.active.postMessage({ type: 'CHECK_UPDATE' }), 3000)
    }).catch(() => {})
  })
}

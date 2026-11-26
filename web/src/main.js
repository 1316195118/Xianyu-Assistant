import { createApp } from 'vue'
import App from './App.vue'
import './style.css'
import './design-system.css'
import './theme'
import { applyPageMeta, publicPageForPath } from './seo'

const publicLoaders = {
  privacy: () => import('./pages/PrivacyPage.vue'),
  terms: () => import('./pages/TermsPage.vue'),
  notFound: () => import('./pages/NotFoundPage.vue')
}

async function mountPage() {
  const page = publicPageForPath(location.pathname)
  if (publicLoaders[page]) {
    applyPageMeta(page, { canonicalPath: location.pathname })
    const component = (await publicLoaders[page]()).default
    createApp(component).mount('#app')
    return
  }
  createApp(App).mount('#app')
}

mountPage().catch((error) => {
  console.error('页面加载失败', error)
  document.getElementById('app').textContent = '页面加载失败，请刷新后重试。'
})

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
  const notifyBuild = (build) => {
    if (!build || build === 'new') return
    try {
      const KEY = 'xy-sw-update-notified-build'
      if (localStorage.getItem(KEY) === build) return
      localStorage.setItem(KEY, build)
    } catch (e) {
      const KEY = 'xy-sw-update-notified-build'
      if (sessionStorage.getItem(KEY) === build) return
      sessionStorage.setItem(KEY, build)
    }
    window.dispatchEvent(new CustomEvent('xy-update', { detail: { build } }))
  }

  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').then((reg) => {
      navigator.serviceWorker.addEventListener('message', (e) => {
        if (e.data && e.data.type === 'UPDATE_AVAILABLE') {
          notifyBuild(String(e.data.build || ''))
        }
      })
      setTimeout(() => {
        const pageBuild = document.querySelector('meta[name="xy-build"]')?.getAttribute('content') || ''
        reg.active && reg.active.postMessage({ type: 'CHECK_UPDATE', build: pageBuild })
      }, 3000)
    }).catch(() => {})
  })
}

import { ref, watch } from 'vue'

const KEY = 'xy-theme'
const VALID = ['system', 'light', 'dark']
const mq = typeof window !== 'undefined' && window.matchMedia
  ? window.matchMedia('(prefers-color-scheme: dark)')
  : null

function read() {
  try {
    const v = localStorage.getItem(KEY)
    if (VALID.includes(v)) return v
  } catch (e) { /* ignore */ }
  return 'system'
}

/** system | light | dark，写入 localStorage 并同步到 <html data-theme> */
export const themeMode = ref(read())

function effective() {
  if (themeMode.value === 'dark') return 'dark'
  if (themeMode.value === 'light') return 'light'
  return mq && mq.matches ? 'dark' : 'light'
}

// 主题切换过渡时长：与 CSS --dur-theme 对齐，留 40ms 余量确保过渡跑完再移除 class。
const THEME_ANIM_MS = 320
// 移动端浏览器 / PWA 状态栏颜色跟随主题，避免深色模式下顶部仍是亮黄一条。
const THEME_META_COLOR = { light: '#f7a600', dark: '#12161c' }
function syncMetaColor(mode) {
  try {
    let meta = document.querySelector('meta[name="theme-color"]')
    if (!meta) {
      meta = document.createElement('meta')
      meta.setAttribute('name', 'theme-color')
      document.head.appendChild(meta)
    }
    meta.setAttribute('content', THEME_META_COLOR[mode] || THEME_META_COLOR.light)
  } catch (e) { /* ignore */ }
}

// 切换瞬间临时挂上 .theme-anim，让背景/文字/边框做一次短过渡；
// 过渡结束后移除，避免常驻 transition 干扰 hover 等交互动画。
let animTimer = null
export function applyTheme(animate = true) {
  const root = document.documentElement
  const mode = effective()
  if (animate) {
    root.classList.add('theme-anim')
    clearTimeout(animTimer)
    animTimer = setTimeout(() => root.classList.remove('theme-anim'), THEME_ANIM_MS)
  }
  root.setAttribute('data-theme', mode)
  syncMetaColor(mode)
}

// 主题扩散动画期间不要再叠加 320ms 的颜色过渡，否则两层动画打架
let suppressThemeAnim = false
export function setThemeInstant(v) {
  suppressThemeAnim = true
  themeMode.value = v
  suppressThemeAnim = false
}

watch(themeMode, (v) => {
  try { localStorage.setItem(KEY, v) } catch (e) { /* ignore */ }
  applyTheme(!suppressThemeAnim)
})

if (mq && mq.addEventListener) {
  mq.addEventListener('change', () => { if (themeMode.value === 'system') applyTheme() })
} else if (mq && mq.addListener) {
  mq.addListener(() => { if (themeMode.value === 'system') applyTheme() })
}

applyTheme(false)

/* ---------- 动效偏好：跟随系统 / 始终减少 / 始终完整 ----------
   系统媒体查询只能表达「现在是否减少」，用户可能想强制开/关；
   这里把结果落到 <html data-motion="reduce|full">，CSS 统一响应这个属性。 */
const MOTION_KEY = 'xy-motion'
const MOTION_VALID = ['system', 'reduce']
const motionMq = typeof window !== 'undefined' && window.matchMedia
  ? window.matchMedia('(prefers-reduced-motion: reduce)')
  : null
function readMotion() {
  try {
    const v = localStorage.getItem(MOTION_KEY)
    if (MOTION_VALID.includes(v)) return v
  } catch (e) { /* ignore */ }
  return 'system'
}
export const motionMode = ref(readMotion())
export function applyMotion() {
  const m = motionMode.value
  const reduce = m === 'reduce' || (m === 'system' && !!motionMq?.matches)
  document.documentElement.setAttribute('data-motion', reduce ? 'reduce' : 'full')
}
watch(motionMode, (v) => {
  try { localStorage.setItem(MOTION_KEY, v) } catch (e) { /* ignore */ }
  applyMotion()
})
const onMotionChange = () => { if (motionMode.value === 'system') applyMotion() }
if (motionMq?.addEventListener) motionMq.addEventListener('change', onMotionChange)
else if (motionMq?.addListener) motionMq.addListener(onMotionChange)
applyMotion()

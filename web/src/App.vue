<script setup>
import { ref, computed, watch, nextTick, onMounted, onUnmounted, defineComponent, defineAsyncComponent, shallowRef, h } from 'vue'
import Overview from './views/Overview.vue'

// 首屏只加载概览；明细 / 联单 / 设置按需异步加载，减小首屏 JS。
const PageSkeleton = {
  name: 'PageSkeleton',
  render() {
    return h('div', { class: 'page' }, [
      h('div', { class: 'card', style: 'height:120px' }),
      h('div', { class: 'card', style: 'height:200px' })
    ])
  }
}
// 分包加载失败不能停在骨架上：给一个明确的失败态 + 可点的出口，
// 否则用户只会看到两块空白卡片，既不知道发生了什么也无法自救。
const LoadFailed = defineComponent({
  name: 'LoadFailed',
  emits: ['retry'],
  setup(_, { emit }) {
    return () => h('div', { class: 'page' }, [
      h('div', { class: 'card load-failed' }, [
        h('div', { class: 'load-failed-ico' }, [
          h('svg', { width: 26, height: 26, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor',
            'stroke-width': 2, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' },
          [h('path', { d: 'M10.3 3.6 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.6a2 2 0 0 0-3.4 0z' }),
            h('line', { x1: 12, y1: 9, x2: 12, y2: 13 }), h('line', { x1: 12, y1: 17, x2: 12.01, y2: 17 })])
        ]),
        h('div', { class: 'load-failed-title' }, '页面没能加载出来'),
        h('div', { class: 'load-failed-sub' }, '网络中断或程序刚更新过，重新载入即可继续。'),
        h('div', { class: 'load-failed-actions' }, [
          h('button', { class: 'btn btn-primary', onClick: () => emit('retry') }, '重试'),
          h('button', { class: 'btn btn-quiet', onClick: () => location.reload() }, '重新载入')
        ])
      ])
    ])
  }
})

// 分包缓存：同一次会话里只取一次；失败则清掉，重试时会重新发请求
// （浏览器的模块注册表会把失败的 import 永久记为 rejected，必须靠这个换新的入口）。
const chunkCache = new Map()
function loadChunk(key, loader) {
  if (chunkCache.has(key)) return chunkCache.get(key)
  const p = loader()
  chunkCache.set(key, p)
  p.catch(() => chunkCache.delete(key))
  return p
}

// 自己实现「加载中 / 失败 / 已就绪」三态，不用 defineAsyncComponent。
// 原因：它把失败判定交给内部 onError/timeout 时序，一旦请求既不成功也不失败
// （被网关挂住、被拦截、离线卡住），页面就会永远停在骨架上——正是用户看到的「空白」。
// 这里额外加 8 秒兜底：迟迟不落地就按失败处理，宁可给出口也不让人干等。
const lazyView = (key, loader) => defineComponent({
  name: 'LazyView',
  props: { refreshKey: { type: Number, default: 0 } },
  setup(props, { attrs }) {
    const comp = shallowRef(null)
    const failed = ref(false)
    let tries = 0
    let watchdog = 0
    const clear = () => { if (watchdog) { clearTimeout(watchdog); watchdog = 0 } }
    const run = () => {
      clear()
      // 兜底：8 秒还没落地就按失败处理，宁可给出口也不让人干等
      watchdog = setTimeout(() => { if (!comp.value) failed.value = true }, 8000)
      loadChunk(key, loader).then((m) => {
        comp.value = m && (m.default || m)
        clear()
      }).catch(() => {
        // 自动重试两次：网络抖一下、部署瞬间 502 多半就此恢复
        if (++tries <= 2) { setTimeout(run, 400 * tries); return }
        clear()
        failed.value = true
      })
    }
    run()
    onUnmounted(clear)
    const retry = () => { tries = 0; failed.value = false; run() }
    return () => {
      if (failed.value) return h(LoadFailed, { onRetry: retry })
      const c = comp.value
      if (!c) return h(PageSkeleton)
      // attrs 里是父组件挂的监听（@edit / @notify 等），必须原样透传
      return h(c, { ...attrs, refreshKey: props.refreshKey })
    }
  }
})
// 懒加载的视图分包：loader 只写一次，预取与路由共用同一份
const viewLoaders = {
  records: () => import('./views/Records.vue'),
  groups: () => import('./views/Groups.vue'),
  consign: () => import('./views/Consign.vue'),
  operations: () => import('./views/Operations.vue'),
  settings: () => import('./views/Settings.vue')
}
const Records = lazyView('records', viewLoaders.records)
const Settings = lazyView('settings', viewLoaders.settings)
const Groups = lazyView('groups', viewLoaders.groups)
const Consign = lazyView('consign', viewLoaders.consign)
const Operations = lazyView('operations', viewLoaders.operations)

// 录入弹窗和工具面板不属于首屏：先让概览和导航出来，再在空闲时预取。
// 真正打开时若网络较慢会继续显示原页面，不在启动阶段阻塞首屏。
const panelLoaders = {
  recordModal: () => import('./components/RecordModal.vue'),
  commandPalette: () => import('./components/CommandPalette.vue'),
  priceCalc: () => import('./components/PriceCalc.vue'),
  reportPanel: () => import('./components/ReportPanel.vue')
}
function lazyPanel(key) {
  return defineAsyncComponent({
    loader: () => loadChunk('panel:' + key, panelLoaders[key]),
    delay: 80,
    timeout: 15000,
    onError(_err, retry, fail, attempts) {
      if (attempts <= 2) setTimeout(retry, 300 * attempts)
      else fail()
    }
  })
}
const RecordModal = lazyPanel('recordModal')
const CommandPalette = lazyPanel('commandPalette')
const PriceCalc = lazyPanel('priceCalc')
const ReportPanel = lazyPanel('reportPanel')
import ConfirmHost from './components/ConfirmHost.vue'
import LoginView from './components/LoginView.vue'
import { api } from './api'
import { currentConfirm, settleConfirm } from './confirm'
import { themeMode } from './theme'
import {
  claimDailyUpdateCheck,
  updateReleaseNotified,
  markUpdateReleaseNotified
} from './updatePreferences'
import ShopSwitcher from './components/ShopSwitcher.vue'
import SiteFooter from './components/SiteFooter.vue'
import { activeId as activeShopId, loadShops, shops, globalSession, hasAnyPassword } from './shopStore'
import { allShopsMode } from './shopScope'
import { shopPickedThisSession, clearShopPicked } from './shopId'
import { gfStatus, gfPanelOpen, gfRebindRequest } from './gfState'
import { applyPageMeta } from './seo'
import {
  online, connected, onRealtime, connectRealtime, disconnectRealtime, clientId
} from './realtime'

const authChecking = ref(true)
const needLogin = ref(false)
const authEnabled = ref(false)
// 首屏超过 6 秒还停在加载态：大概率是服务刚重启 / 网络半断，把出口给出来，
// 不能让用户对着一个转圈的白屏干等。
const slowBoot = ref(false)
let slowBootTimer = 0
let dailyUpdateTimer = 0
let inputBlurTimer = 0
const inputFocused = ref(false)
const KEYBOARD_INPUT_SELECTOR = [
  'input:not([type="button"]):not([type="submit"]):not([type="reset"]):not([type="checkbox"]):not([type="radio"]):not([type="range"]):not([type="color"]):not([type="file"])',
  'textarea',
  '[contenteditable]:not([contenteditable="false"])'
].join(', ')
function isKeyboardInput(target) {
  return target instanceof HTMLElement && target.matches(KEYBOARD_INPUT_SELECTOR)
}
function syncInputFocus() {
  inputFocused.value = isKeyboardInput(document.activeElement)
}
function onInputFocusIn(event) {
  if (!isKeyboardInput(event.target)) return
  if (inputBlurTimer) clearTimeout(inputBlurTimer)
  inputFocused.value = true
}
function onInputFocusOut(event) {
  if (!isKeyboardInput(event.target)) return
  if (inputBlurTimer) clearTimeout(inputBlurTimer)
  // 焦点可能从一个输入框直接移到另一个，等浏览器更新 activeElement 后再判断
  inputBlurTimer = window.setTimeout(syncInputFocus, 0)
}
function hardReload() {
  try { location.reload() } catch (e) { location.href = '/' }
}

// 同步状态横幅：顶栏那个 6px 小圆点太容易被忽略，断线后别人改了数据，
// 自己这边一直不刷新却不知道为什么。这里给一条明确的说明。
// 只对「连上过又断了」提示，首屏建连期间不误报；恢复后再亮 1.8 秒收尾。
const highlightId = ref(0)          // 刚保存的记录 id，传给明细页做定位高亮
const syncState = ref('ok')          // ok | down | back
let syncDownTimer = 0
let syncBackTimer = 0
// 以「注册监听那一刻」的状态作为初值：连接可能在本组件挂载前就已建立，
// 只靠 watch 的上升沿会漏掉，导致断线时误判成「从没连上过」而不提示。
let syncEverUp = connected.value
watch(connected, (up) => {
  if (up) {
    clearTimeout(syncDownTimer)
    syncEverUp = true
    if (syncState.value === 'down') {
      syncState.value = 'back'
      clearTimeout(syncBackTimer)
      syncBackTimer = setTimeout(() => { syncState.value = 'ok' }, 1800)
    }
    return
  }
  if (!syncEverUp) return
  clearTimeout(syncDownTimer)
  syncDownTimer = setTimeout(() => {
    if (!connected.value) { syncState.value = 'down'; clearTimeout(syncBackTimer) }
  }, 2000)
}, { immediate: true })

async function checkAuth() {
  try {
    const s = await api.authStatus()
    authEnabled.value = !!s.global || (s.shops || []).some(x => x.has_password)
    hasAnyPassword.value = authEnabled.value
    // 有会话但不是全局会话 → 被锁在单店；没会话时按可自由切换处理（登录后会纠正）
    globalSession.value = !s.has_session || !!s.global_session
    if (!globalSession.value) allShopsMode.value = false
    const hasSession = !!s.has_session
    const shopCount = (s.shops || []).length
    let picked = 0
    try { picked = parseInt(localStorage.getItem('xy-shop') || '0', 10) || 0 } catch (e) { picked = 0 }

    // 只要系统里有任何一个店铺设了密码，每次新开会话都必须先选店——
    // 否则会直接进上次那家，别人拿到链接就绕过了店铺选择。
    const anyShopPwd = (s.shops || []).some(x => x.has_password)
    const pickedThisSession = shopPickedThisSession()

    // 只有一个店铺、且没有任何店铺密码时，不必每次选，自动进入
    if (shopCount === 1 && !picked && !anyShopPwd) {
      picked = s.shops[0].id
      try { localStorage.setItem('xy-shop', String(picked)) } catch (e) {}
    }
    const needPick = anyShopPwd ? !pickedThisSession : !picked
    needLogin.value = (s.global && !hasSession) || needPick
  } catch (e) {
    needLogin.value = false
  } finally {
    authChecking.value = false
  }
}
function onUnauthorized() { needLogin.value = true; showModal.value = false }

// 首屏不一次性拉取所有视图和弹窗。只对用户即将进入的导航做轻量预取，
// 既保留点击后的即时反馈，也避免移动网络首访时无意义地下载全部分包。
function prefetchView(key) {
  if (navigator.onLine === false || !viewLoaders[key]) return
  try { loadChunk(key, viewLoaders[key]).catch(() => {}) } catch (e) { /* 忽略预取失败 */ }
}
function prefetchPanel(key) {
  if (navigator.onLine === false || !panelLoaders[key]) return
  try { loadChunk('panel:' + key, panelLoaders[key]).catch(() => {}) } catch (e) { /* 忽略预取失败 */ }
}

// ---------- 多人实时同步 ----------
// 收到「别人」的变更后自动重拉当前视图；自己触发的变更已由本地流程刷新，跳过。
// 批量操作会连续产生多条事件，用 2.5s 节流避免提示刷屏。
let offRealtime = null
let lastSyncToast = 0
// 别人连续改了好几条时，事件会一条条来；每条都触发整页重拉就是白白多拉几次。
// 300ms 内合并成一次刷新，体感完全一样，请求量按事件数下降。
let bumpTimer = null
let bumpPending = false
function bumpCoalesced() {
  if (bumpTimer) clearTimeout(bumpTimer)
  bumpTimer = setTimeout(() => {
    bumpTimer = null
    // 页面在后台时先攒着：后台刷新既看不见，又白白占带宽和手机电量
    if (document.hidden) { bumpPending = true; return }
    bump()
  }, 300)
}
function onVisibilityChange() {
  if (!document.hidden && bumpPending) { bumpPending = false; bump() }
}
document.addEventListener('visibilitychange', onVisibilityChange)
// 顶栏闲鱼状态点：在任意页面都能看到绑定是否失效，点一下直达绑定弹窗
async function loadGfStatus() {
  try {
    const s = await api.goofishStatus()
    gfStatus.value = {
      logged_in: !!s.logged_in,
      session_valid: !!s.session_valid,
      order_api_valid: !!s.order_api_valid,
      expired: !!s.expired,
      sync_error: s.sync_error || '',
      nick: s.nick || '',
      bridge_online: !!s.bridge_online
    }
  } catch (e) { /* 未选店 / 未登录时顶栏不显示状态，不影响主流程 */ }
}
function openGfPanel(rebind = false) {
  switchView('settings')
  gfRebindRequest.value = !!rebind
  gfPanelOpen.value = true
}
const gfBad = computed(() => !!gfStatus.value.expired || !!gfStatus.value.sync_error)
const gfText = computed(() => gfBad.value ? '需恢复' : (gfStatus.value.order_api_valid ? '闲鱼' : (gfStatus.value.logged_in ? '待检测' : '去绑定')))
const gfAriaLabel = computed(() => `闲鱼绑定状态：${gfText.value}`)
const gfTip = computed(() => {
  if (gfBad.value) return '闲鱼登录已失效：' + (gfStatus.value.sync_error || '请在浏览器中重新登录闲鱼') + '，点击查看恢复方式'
  if (gfStatus.value.order_api_valid) return `闲鱼订单接口可用「${gfStatus.value.nick}」，点击查看同步状态`
  if (gfStatus.value.logged_in) return '闲鱼凭证存在，但订单接口尚未验证，点击查看'
  return '闲鱼未绑定，点击去绑定'
})

function startRealtime() {
  connectRealtime()
  offRealtime = onRealtime((ev) => {
    if (!ev || ev.kind !== 'change') return
    // 闲鱼自动同步失败：Cookie 失效，主动弹醒目提示，不用等用户手动同步才发现
    if (ev.path === '/api/goofish/status') {
      loadGfStatus()
      if (!/失败|失效/.test(ev.summary || '')) {
        bump()
        return
      }
      notify(ev.summary || '闲鱼同步失效，请在浏览器中重新登录闲鱼', true,
        { label: '去绑定', ttl: 15000, run: () => openGfPanel(true) },
        { tone: 'error' })
      bump()
      return
    }
    if (ev.actor && ev.actor === clientId) return
    bumpCoalesced()
    // 正在编辑时提醒冲突风险，但不打断输入
    const now = Date.now()
    if (now - lastSyncToast < 2500) return
    lastSyncToast = now
    notify(`其他成员${ev.summary || '更新了数据'}，已自动刷新`, false, null, { tone: 'info' })
  })
}

// 切换店铺后所有视图都要重拉：refreshKey 是既有的一级刷新开关，
// 复用它可以保证不留下任何按旧店铺渲染的残留状态。
// 切店要做两件事：刷新所有视图 + 换一条实时事件流。
// 不换流的话，会继续收到上一个店铺的变更通知并触发无意义刷新。
watch(activeShopId, () => {
  // 切店必须清掉上一家店的「跳转过滤」：商品名 / 归一化键 / 预设类型 / 定位记录
  // 都属于旧店，留着会让新店的明细被旧条件卡住（看起来像只有一两条数据）。
  focusQuery.value = ''
  focusNorm.value = ''
  focusType.value = ''
  highlightId.value = 0
  // 当前页 key 已包含 activeShopId，切店会立即重建并自行拉取新店数据。
  // 这里再 bump 一次只会让旧页面先发一轮、再到新页面发同一轮，增加点击后的主线程工作。
  loadTodoCount()
  loadGfStatus()
  connectRealtime()
})

function scheduleDailyUpdateCheck(delay = 3500) {
  if (dailyUpdateTimer) clearTimeout(dailyUpdateTimer)
  dailyUpdateTimer = setTimeout(runDailyUpdateCheck, delay)
}

async function runDailyUpdateCheck() {
  dailyUpdateTimer = 0
  if (!globalSession.value) return
  if (!claimDailyUpdateCheck()) return
  try {
    const result = await api.updateCheck()
    if (!result?.has_update || !result.latest || updateReleaseNotified(result)) return
    markUpdateReleaseNotified(result)
    notify(
      `发现新版本 v${result.latest}，可前往设置查看更新`,
      false,
      { label: '查看更新', ttl: 12000, run: () => switchView('settings') },
      { tone: 'info', ttl: 12000 }
    )
  } catch (e) {
    // 每日检查在后台静默进行；断网或更新源暂时不可用时不在主界面打扰用户。
  }
}

watch([authChecking, needLogin], ([checking, login]) => {
  if (!checking && !login) scheduleDailyUpdateCheck()
}, { immediate: true })
onMounted(() => {
  slowBootTimer = setTimeout(() => { if (authChecking.value) slowBoot.value = true }, 6000)
  loadTodoCount()
  loadGfStatus()
  checkAuth()
  loadShops()
  startRealtime()
  window.addEventListener('xy-unauthorized', onUnauthorized)
  window.addEventListener('hashchange', onHash)
  window.addEventListener('keydown', onGlobalKey)
  window.addEventListener('keydown', onShortcut)
  window.addEventListener('xy-update', onUpdate)
  window.addEventListener('beforeinstallprompt', onBeforeInstall)
  window.addEventListener('scroll', onScroll, { passive: true })
  window.addEventListener('touchstart', onPullStart, { passive: true })
  window.addEventListener('touchmove', onPullMove, { passive: false })
  window.addEventListener('touchend', onPullEnd, { passive: true })
  window.addEventListener('touchcancel', onPullEnd, { passive: true })
  window.addEventListener('focusin', onInputFocusIn)
  window.addEventListener('focusout', onInputFocusOut)
  window.addEventListener('paste', onPaste)
  setTimeout(consumeSharedImage, 500)
  onScroll()
})
onUnmounted(() => {
  document.removeEventListener('visibilitychange', onVisibilityChange)
  if (bumpTimer) clearTimeout(bumpTimer)
  bumpTimer = null
  if (dailyUpdateTimer) clearTimeout(dailyUpdateTimer)
  dailyUpdateTimer = 0
  if (inputBlurTimer) clearTimeout(inputBlurTimer)
  inputBlurTimer = 0
  offRealtime?.()
  offRealtime = null
  disconnectRealtime()
  window.removeEventListener('xy-unauthorized', onUnauthorized)
  window.removeEventListener('hashchange', onHash)
  window.removeEventListener('keydown', onGlobalKey)
  window.removeEventListener('keydown', onShortcut)
  window.removeEventListener('xy-update', onUpdate)
  window.removeEventListener('beforeinstallprompt', onBeforeInstall)
  window.removeEventListener('scroll', onScroll)
  window.removeEventListener('touchstart', onPullStart)
  window.removeEventListener('touchmove', onPullMove)
  window.removeEventListener('touchend', onPullEnd)
  window.removeEventListener('touchcancel', onPullEnd)
  window.removeEventListener('focusin', onInputFocusIn)
  window.removeEventListener('focusout', onInputFocusOut)
  window.removeEventListener('paste', onPaste)
  document.body.classList.remove('overlay-open')
})
function onLoginSuccess() { needLogin.value = false; bump() }
async function doLogout() {
  try { await api.logout() } catch (e) { /* ignore */ }
  clearShopPicked()
  needLogin.value = true
}

const validViews = ['overview', 'records', 'groups', 'consign', 'operations', 'settings']
const hashView = (h) => {
  const v = h.replace('#', '').replace('-add', '')
  return validViews.includes(v) ? v : 'overview'
}
const view = ref(hashView(location.hash))
watch([view, needLogin, authChecking], ([currentView, login, checking]) => {
  if (checking) return
  applyPageMeta(login ? 'login' : currentView, { canonicalPath: '/' })
}, { immediate: true })
const showModal = ref(location.hash.endsWith('-add'))
const viewOnly = ref(false)          // 弹窗是否以「只读查看」打开
const focusQuery = ref('')           // 从商品利润排行跳明细时要过滤的商品名
const focusNorm = ref('')            // 该商品的归一化名（product_norm），用于精确过滤整组单据
const focusType = ref('')            // 非空 = 跳过去时直接按类型过滤（如 unlinked 未挂成本）
const focusNonce = ref(0)            // 每次跳转自增：明细页挂载时也能收到，且连点同一商品仍会重新过滤
const consignTab = ref('sell')       // 从外部订单跳寄卖页时，直接打开对应账本
const consignFocusNonce = ref(0)
const operationsTab = ref('health')  // 设置页快捷入口可直接落到经营中心对应页签
const settingsFocusPanel = ref('')   // 待办跳设置时，直接打开同步或备份面板
const settingsFocusNonce = ref(0)
const editing = ref(null)
const sharedFile = ref(null)         // 分享目标 / 粘贴截图带入录入弹窗
const refreshKey = ref(0)
const toasts = ref([])
const focusGroupId = ref(null)   // 从明细点联单标签时，联动定位到该联单
let toastSeq = 0

// ---------- 命令面板 / 顶栏阴影 / 视图滚动记忆 ----------
const cmdOpen = ref(false)
const calcOpen = ref(false)   // 定价速算弹窗
const reportOpen = ref(false) // 周报月报面板
const calcPrefill = ref(null) // 定价速算带进录入弹窗的值
const todoCount = ref(0)      // 未挂成本卖单数，明细页脚标
const scrolled = ref(false)
const scrollPos = {}
let pendingScroll = null
// 回到顶部：手机端列表翻过一屏后，回顶只能一路往上划，给个按钮
const showToTop = ref(false)
function onScroll() {
  scrolled.value = window.scrollY > 4
  showToTop.value = window.scrollY > 420
}
function toTop() {
  showToTop.value = false
  try { window.scrollTo({ top: 0, behavior: 'smooth' }) } catch (e) { window.scrollTo(0, 0) }
}

// 手机下拉刷新：只在页面顶部、没有弹窗时生效，跟手位移 + 松手回弹
const pullDist = ref(0)
const pullBusy = ref(false)
let pullStart = null
function pullBlocked() {
  if (overlayOpen.value) return true
  return !!document.querySelector('.modal-mask, .ali-mask, .confirm-panel')
}
function onPullStart(e) {
  if (pullBusy.value || pullBlocked() || window.scrollY > 0 || e.touches.length !== 1) return
  pullStart = e.touches[0].clientY
}
function onPullMove(e) {
  if (pullStart == null) return
  const d = e.touches[0].clientY - pullStart
  if (d <= 0) { pullDist.value = 0; return }
  if (e.cancelable) e.preventDefault()
  pullDist.value = Math.min(96, d * 0.5)
}
function onPullEnd() {
  if (pullStart == null) return
  pullStart = null
  if (pullDist.value >= 56) {
    pullBusy.value = true
    pullDist.value = 56
    bump()
    loadTodoCount()
    loadGfStatus()
    setTimeout(() => { pullBusy.value = false; pullDist.value = 0 }, 800)
  } else {
    pullDist.value = 0
  }
}

// ---------- PWA 安装提示 ----------
const installEvt = ref(null)
const showInstall = ref(false)

// 批量导入面板：组件本身按需加载（首屏少背十几 KB），
// 加载失败给一条 toast，不要像以前那样卡在空白上。
const batchOpen = ref(false)
const BatchImportComp = shallowRef(null)
async function openBatch() {
  if (!BatchImportComp.value) {
    try {
      const m = await import('./components/BatchImport.vue')
      BatchImportComp.value = m.default || m
    } catch (e) {
      notify('批量导入组件加载失败，请检查网络后重试', true)
      return
    }
  }
  batchOpen.value = true
}
// 装不装是用户自己的事，但不该无限期地劝：从第一次提示起最多劝 7 天，
// 之后自动不再出现（想装随时能从浏览器菜单装）。点「以后」则是永久不再提示。
const INSTALL_TTL = 7 * 24 * 60 * 60 * 1000
function installPromptExpired() {
  let first = 0
  try { first = parseInt(localStorage.getItem('xy-install-first') || '0', 10) || 0 } catch (e) { first = 0 }
  if (!first) {
    try { localStorage.setItem('xy-install-first', String(Date.now())) } catch (e) { /* 隐私模式下写不进去，忽略 */ }
    return false
  }
  return Date.now() - first > INSTALL_TTL
}
function onBeforeInstall(e) {
  e.preventDefault()
  installEvt.value = e
  try {
    if (localStorage.getItem('xy-install-dismissed')) return
  } catch (err) { return }
  if (installPromptExpired()) return
  showInstall.value = true
}
async function doInstall() {
  if (!installEvt.value) return
  installEvt.value.prompt()
  const { outcome } = await installEvt.value.userChoice
  installEvt.value = null
  showInstall.value = false
  notify(outcome === 'accepted' ? '已安装到桌面' : '已取消安装', false, null, { tone: outcome === 'accepted' ? 'success' : 'info' })
}
function dismissInstall() {
  showInstall.value = false
  localStorage.setItem('xy-install-dismissed', '1')
}

// 任意遮罩层（录单弹窗 / 确认框）打开时锁住背景滚动，
// 避免桌面端出现双滚动条、移动端背景滚动穿透
const overlayOpen = computed(() => showModal.value || !!currentConfirm.value || cmdOpen.value || batchOpen.value || calcOpen.value || reportOpen.value)
watch(overlayOpen, (open) => {
  document.body.classList.toggle('overlay-open', open)
}, { immediate: true })

const navItems = [
  { key: 'overview', label: '概览', icon: 'M3 12h4l3-8 4 16 3-8h4' },
  { key: 'records', label: '明细', icon: 'M4 6h16M4 12h16M4 18h16' },
  { key: 'groups', label: '联单', icon: 'M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7' },
  { key: 'consign', label: '寄卖', icon: 'M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM23 21v-2a4 4 0 0 0-3-3.9M16 3.1a4 4 0 0 1 0 7.8' },
  { key: 'operations', label: '经营', icon: 'M4 20V10M10 20V4M16 20v-7M22 20H2' },
  { key: 'settings', label: '设置', icon: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.6 1.6 0 0 0 .3 1.8l.1.1-2.8 2.8-.1-.1a1.6 1.6 0 0 0-1.8-.3 1.6 1.6 0 0 0-1 1.5V21h-4v-.2a1.6 1.6 0 0 0-1-1.5 1.6 1.6 0 0 0-1.8.3l-.1.1-2.8-2.8.1-.1a1.6 1.6 0 0 0 .3-1.8 1.6 1.6 0 0 0-1.5-1H3v-4h.2a1.6 1.6 0 0 0 1.5-1 1.6 1.6 0 0 0-.3-1.8l-.1-.1 2.8-2.8.1.1a1.6 1.6 0 0 0 1.8.3H9a1.6 1.6 0 0 0 1-1.5V3h4v.2a1.6 1.6 0 0 0 1 1.5 1.6 1.6 0 0 0 1.8-.3l.1-.1 2.8 2.8-.1.1a1.6 1.6 0 0 0-.3 1.8V9a1.6 1.6 0 0 0 1.5 1H21v4h-.2a1.6 1.6 0 0 0-1.4 1z' }
]
const mobileNavItems = navItems.filter((n) => n.key !== 'operations')
const pageTitle = computed(() => ({ overview: '经营概览', records: '交易明细', groups: '联动单', consign: '寄卖', operations: '经营中心', settings: '系统设置' }[view.value]))
const pageSub = computed(() => ({
  overview: '实时掌握利润、成本与费用构成',
  records: '按交易时间排序 · 检索 / 筛选 / 批量管理',
  groups: '多买多卖挂同一个联单 · 看哪类商品利润高',
  consign: '寄卖代卖 + 垫钱代采 · 按人查账结算，不计入自己的利润成本',
  operations: '数据体检、物流运费、服务费核销、对账单、库龄与智能定价',
  settings: '识别引擎、安全、备份与数据'
}[view.value]))

// 命令面板条目：页面跳转 + 常用动作 + 主题切换
const commands = computed(() => [
  { id: 'nav-overview', label: '经营概览', desc: '利润、成本与费用构成', icon: navItems[0].icon, hint: '页面', keywords: 'overview 首页 概览', run: () => switchView('overview') },
  { id: 'nav-records', label: '交易明细', desc: '检索 / 筛选 / 批量管理', icon: navItems[1].icon, hint: '页面', keywords: 'records 明细 记录 列表', run: () => switchView('records') },
  { id: 'nav-groups', label: '联动单', desc: '多买多卖挂同一联单', icon: navItems[2].icon, hint: '页面', keywords: 'groups 联单 联动', run: () => switchView('groups') },
  { id: 'nav-consign', label: '寄卖', desc: '寄卖 / 代采两本账，按人结算', icon: navItems[3].icon, hint: '页面', keywords: 'consign 寄卖 代卖 代采 垫付 归属 张三 李四', run: () => switchView('consign') },
  { id: 'nav-operations', label: '经营中心', desc: '体检、物流、费用、对账、库龄与定价', icon: navItems[4].icon, hint: '页面', keywords: 'operations 经营 体检 物流 运费 服务费 对账 库龄 定价 补货 多店', run: () => switchView('operations') },
  { id: 'nav-settings', label: '系统设置', desc: '识别引擎、安全、备份与数据', icon: navItems[5].icon, hint: '页面', keywords: 'settings 设置 配置 备份', run: () => switchView('settings') },
  { id: 'add', label: '上传截图录入', desc: '新建一条买卖记录', icon: 'M12 5v14M5 12h14', hint: 'N', keywords: 'add new 新建 录入 上传', run: openAdd },
  { id: 'batch', label: '批量导入截图', desc: '一次识别多张订单截图', icon: 'M12 16V4M7 9l5-5 5 5M4 16v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2', hint: '批量', keywords: 'batch 批量 多张 导入 截图', run: openBatch },
  { id: 'theme-light', label: '切换到浅色模式', desc: '固定使用浅色主题', icon: 'M12 4V2M12 22v-2M4 12H2M22 12h-2M5.6 5.6 4.2 4.2M19.8 19.8l-1.4-1.4M18.4 5.6l1.4-1.4M4.2 19.8l1.4-1.4M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z', hint: '主题', keywords: 'theme light 浅色 亮色', run: () => { themeMode.value = 'light' } },
  { id: 'theme-dark', label: '切换到深色模式', desc: '固定使用深色主题', icon: 'M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z', hint: '主题', keywords: 'theme dark 深色 夜间 暗色', run: () => { themeMode.value = 'dark' } },
  { id: 'theme-system', label: '跟随系统主题', desc: '随系统深浅色自动切换', icon: 'M4 4h16v12H4zM8 20h8M12 16v4', hint: '主题', keywords: 'theme system auto 系统 自动', run: () => { themeMode.value = 'system' } },
  { id: 'export', label: '导出数据', desc: '前往设置 → 数据导出 / 导入', icon: 'M12 3v12M7 10l5 5 5-5M5 21h14', hint: '设置', keywords: 'export csv json xlsx 导出 备份', run: () => switchView('settings') },
  { id: 'price-calc', label: '定价速算', desc: '按成本算售价，或按售价算利润', icon: 'M12 2v20M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6', hint: '工具', keywords: 'price 定价 售价 利润 算价 计算 工具', run: () => { calcOpen.value = true } },
  { id: 'report', label: '生成周报 / 月报', desc: '一键整理可复制的经营小结', icon: 'M8 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2h-3M8 3v4M16 3v4M4 9h16M9 14l2 2 4-4', hint: '工具', keywords: 'report 周报 月报 报告 总结 复制 工具', run: () => { reportOpen.value = true } }
])

function setHash(h) { if (location.hash !== h) history.replaceState(null, '', h) }
function hashFor(v, add = false) { return '#' + v + (add ? '-add' : '') }
function onHash() {
  const h = location.hash
  if (h.endsWith('-add')) { editing.value = null; showModal.value = true; return }
  showModal.value = false
  const v = hashView(h)
  if (v === view.value) return
  scrollPos[view.value] = window.scrollY
  view.value = v
  pendingScroll = scrollPos[v] || 0
}
// 反馈队列：连续操作不再互相覆盖，最多同时保留 3 条；带撤销动作的提示停留更久
function notify(text, err = false, action = null, opts = {}) {
  const id = ++toastSeq
  const ttl = action?.ttl || opts.ttl || (action ? 9000 : 3200)
  toasts.value.push({
    id, text, err, action, ttl, busy: false,
    tone: opts.tone || (err ? 'error' : 'success'),
    icon: opts.icon || null
  })
  if (toasts.value.length > 3) toasts.value.splice(0, toasts.value.length - 3)
  setTimeout(() => removeToast(id), ttl)
}
function removeToast(id) {
  const i = toasts.value.findIndex(t => t.id === id)
  if (i >= 0) toasts.value.splice(i, 1)
}
async function runToastAction(t) {
  if (!t.action || t.busy) return
  t.busy = true
  try { await t.action.run() }
  catch (e) { notify('撤销失败：' + (e.message || '未知错误'), true) }
  finally { t.busy = false; removeToast(t.id) }
}
function bump() { refreshKey.value++ }
async function loadTodoCount() {
  try { todoCount.value = Number((await api.todoCount()).unlinked) || 0 } catch (e) { todoCount.value = 0 }
}
watch(refreshKey, loadTodoCount)
function openAdd(prefill = null, initialFile = null) {
  // @click="openAdd" 会把 MouseEvent 传进来，不能当成预填数据
  const pf = prefill && typeof prefill === 'object' && !(prefill instanceof Event) && 'sell_price' in prefill ? prefill : null
  editing.value = null
  calcPrefill.value = pf
  sharedFile.value = initialFile instanceof Blob ? initialFile : null
  showModal.value = true
  setHash(hashFor(view.value, true))
}
function openAddWithFile(file) {
  if (!(file instanceof Blob)) return
  openAdd(null, file)
  notify('截图已带入，正在识别…', false, null, { tone: 'info' })
}
function onPaste(e) {
  if (!e.clipboardData || showModal.value) return
  const item = Array.from(e.clipboardData.items || []).find((x) => x.kind === 'file' && x.type.startsWith('image/'))
  if (!item) return
  const file = item.getAsFile()
  if (!file) return
  e.preventDefault()
  openAddWithFile(file)
}
async function consumeSharedImage() {
  if (!('caches' in window)) return
  try {
    const cache = await caches.open('xianyu-share-v1')
    const key = '/__xy-shared-image__'
    const res = await cache.match(key)
    if (!res) return
    const blob = await res.blob()
    await cache.delete(key)
    if (!blob.size) return
    const type = res.headers.get('X-Image-Type') || blob.type || 'image/png'
    const name = res.headers.get('X-Image-Name') || 'shared-order.png'
    openAddWithFile(new File([blob], name, { type }))
  } catch (e) { /* 分享目标不可用时静默，仍可粘贴或选择图片 */ }
}
// 定价速算「用这个价录入」：关掉工具，带着结果打开录入弹窗
function onCalcUse(data) { openAdd(data) }
function openEdit(r) { viewOnly.value = false; editing.value = r; showModal.value = true }
// 联单里点订单：默认「查看」，要看腻了再点弹窗里的「编辑」。
// 和明细/联单页显式的编辑按钮区分开，避免手一滑就改到账。
function openViewRecord(r) { viewOnly.value = true; editing.value = r; showModal.value = true }
// 关闭时先让 Transition 走完离场动画，再清空编辑数据，避免淡出过程中表单闪成空白
function closeModal() { showModal.value = false; viewOnly.value = false; calcPrefill.value = null; sharedFile.value = null; setHash(hashFor(view.value)) }
function clearEditing() { editing.value = null }
// 统一键盘调度：确认框打开时不干预，Esc 交由 ConfirmHost 处理
function onGlobalKey(e) {
  if (e.key !== 'Escape') return
  if (currentConfirm.value) { settleConfirm(currentConfirm.value.id, false); return }
  if (showModal.value) closeModal()
}
// 快捷键：/ 聚焦搜索、n 新建；输入态与弹窗打开时不拦截
function onShortcut(e) {
  if (currentConfirm.value || showModal.value) return
  // Ctrl/⌘ + K：命令面板，输入态也允许唤出
  if ((e.ctrlKey || e.metaKey) && (e.key === 'k' || e.key === 'K')) {
    e.preventDefault()
    cmdOpen.value = true
    return
  }
  // 命令面板已打开时，不再把 / n 等快捷键透传到页面
  if (cmdOpen.value) return
  const tag = (e.target?.tagName || '').toLowerCase()
  if (tag === 'input' || tag === 'textarea' || tag === 'select' || e.target?.isContentEditable) return
  if (e.key === '/') {
    e.preventDefault()
    document.querySelector('.filter-main .search-wrap .input')?.focus()
  } else if (e.key === 'n' || e.key === 'N') {
    e.preventDefault(); openAdd()
  }
}
function onUpdate() { notify('发现新版本，刷新页面即可更新', false, null, { tone: 'info' }) }
function onBatchDone(n) {
  // 不在这里关闭面板：用户还要看「成功 N 条 / 失败原因」的汇总，
  // 由面板上的「完成」按钮负责关闭。
  bump()
  if (n > 0) notify(`已批量导入 ${n} 条记录`)
}
function onSaved(info) {
  closeModal()
  // 保存后一律不定位：以前「新增」会把列表过滤成只剩刚存的那一条（顶上一个
  // 「已定位到指定记录」的横条），用户看不到别的单，还得手动点「显示全部」。
  // 现在只刷新列表，保持原来的筛选和位置。需要精确定位时走「最近记录」跳转。
  bump()
  if (info?.incomplete?.length) {
    const savedShop = activeShopId.value
    const action = info.id ? { label: '查看该记录', run: () => {
      if (activeShopId.value === savedShop) gotoRecord(info.id)
      else notify('请切回保存记录时的店铺查看', false, null, { tone: 'info' })
    } } : null
    notify(`记录已保存，但${info.incomplete.join('、')}失败。不用重复录入，可到明细补传截图或关联联单。`, false, action, { tone: 'warn', ttl: 9000 })
  } else if (info?.merged) notify(`已自动合并到同名商品「${info.product}」`)
  else if (info?.autoGroup) notify(`已保存，并自动生成联单「${info.product}」`)
  else notify(info?.editing ? '已保存修改' : '已保存')
}
// 记住每个视图的滚动位置，切回来时恢复；首次进入回到顶部。
// 恢复时机放在入场动画结束后（onPageEntered）；此处再短轮询，
// 兜住数据异步加载导致的高度变化，避免 scrollTo 被截断。
function applyScroll(target) {
  let tries = 0
  const tick = () => {
    window.scrollTo(0, target)
    if (Math.abs(window.scrollY - target) <= 2 || ++tries >= 10) return
    setTimeout(tick, 90)
  }
  tick()
}
function switchView(v) {
  if (v === view.value) return
  scrollPos[view.value] = window.scrollY
  view.value = v
  setHash(hashFor(v))
  pendingScroll = scrollPos[v] || 0
}
function gotoConsign(source) {
  consignTab.value = source === '代采' || source === '代买' ? 'buy' : 'sell'
  consignFocusNonce.value++
  switchView('consign')
}
function gotoOperations(tab = 'health') {
  operationsTab.value = tab || 'health'
  switchView('operations')
}
function gotoSettings(panel = '') {
  settingsFocusPanel.value = panel || ''
  settingsFocusNonce.value++
  switchView('settings')
}
// 旧页面在入场动画结束后才挂载、高度才稳定，此时恢复滚动位置；
// applyScroll 内部再短轮询，兜住数据异步加载导致的高度变化。
// 现在没有 Transition 的 after-enter 钩子，改为视图切换后 nextTick 触发。
function onPageEntered() {
  if (pendingScroll == null) return
  const target = pendingScroll
  pendingScroll = null
  applyScroll(target)
}
watch(view, () => { nextTick(() => requestAnimationFrame(onPageEntered)) })
function gotoGroup(id) { focusGroupId.value = id; switchView('groups') }
// 首页「最近记录」点商品名：跳到明细并把那条记录高亮闪电一下
// 首页「商品利润排行」点某一行：跳到明细并按该商品过滤。
// 传 { name, norm } 两个值：name 用于搜索框回显，norm 用于精确定位整组（同名不同写法）。
function gotoProduct(p) {
  const name = typeof p === 'string' ? p : (p && p.name) || ''
  const norm = typeof p === 'string' ? '' : (p && p.norm) || ''
  if (!name) return
  focusQuery.value = name
  focusNorm.value = norm
  focusType.value = ''
  focusNonce.value++
  switchView('records')
}
// 跳到明细并带一个预设筛选（首页「经营建议」用），例如只看未挂成本的卖单
function gotoRecords(preset) {
  focusQuery.value = ''
  focusNorm.value = ''
  focusType.value = (preset && preset.type) || ''
  focusNonce.value++
  switchView('records')
}
function gotoRecord(id) {
  if (!id) return
  highlightId.value = id
  switchView('records')
}
function goSettingsFromModal() {
  closeModal(); switchView('settings')
  notify('可在「识别引擎」中切换，然后重新上传截图')
}
</script>

<template>
  <div v-if="authChecking" class="auth-loading">
    <span class="brand-mark">鱼</span>
    <span class="spin"></span>
    <span class="muted">{{ slowBoot ? '服务器没有响应' : '正在加载…' }}</span>
    <button v-if="slowBoot" class="btn btn-sm" @click="hardReload">刷新重试</button>
  </div>
  <LoginView v-else-if="needLogin" @success="onLoginSuccess" />
  <div v-else class="app-shell" :class="{ 'install-on': showInstall }">
    <!-- 桌面侧边栏 -->
    <aside class="sidebar">
      <div class="side-brand">
        <span class="brand-mark">鱼</span>
        <span class="side-brand-text"><b>闲鱼售卖统计</b><small>经营数据看板</small></span>
      </div>
      <nav class="side-nav">
        <button v-for="n in navItems" :key="n.key" :class="{ active: view === n.key }" :aria-current="view === n.key ? 'page' : undefined"
                @mouseenter="prefetchView(n.key)" @focus="prefetchView(n.key)" @click="switchView(n.key)">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path :d="n.icon" /></svg>
          <span>{{ n.label }}</span>
        </button>
      </nav>
      <div class="side-foot">
        <button class="side-add" @mouseenter="prefetchPanel('recordModal')" @focus="prefetchPanel('recordModal')" @click="openAdd">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
          上传截图录入
        </button>
        <button v-if="authEnabled" class="side-logout" @click="doLogout">退出登录</button>
        <a class="side-legal" href="/privacy">隐私政策</a>
        <div class="side-ver muted">数据仅存你的服务器 · 离线可用</div>
      </div>
    </aside>

    <!-- 主内容 -->
    <main class="main">
      <header class="topbar" :class="{ scrolled }">
        <div class="topbar-mobile-brand"><span class="brand-mark sm">鱼</span></div>
        <div class="topbar-titles">
          <h1>{{ pageTitle }}</h1>
          <p class="muted">{{ pageSub }}</p>
        </div>
        <div class="topbar-actions">
          <button v-if="shops.length > 1 && globalSession" class="scope-pill" :class="{ on: allShopsMode }"
                  :title="allShopsMode ? '当前查看全部店铺合计，点击切回单店' : '当前只看这家店，点击切换为全部店铺合计'"
                  @click="allShopsMode = !allShopsMode">
            {{ allShopsMode ? '全部店铺' : '当前店铺' }}
          </button>
          <ShopSwitcher @notify="notify" />
          <span class="presence" :class="{ live: connected }"
                :title="connected ? `实时同步已连接 · ${online} 个客户端在线` : '实时同步未连接，正在自动重连…'">
            <i class="presence-dot" aria-hidden="true"></i>
            <span class="presence-text">{{ connected ? (online > 1 ? `${online} 人在线` : '实时同步') : '离线' }}</span>
          </span>
          <button class="gf-presence" :class="{ bad: gfBad, on: gfStatus.order_api_valid }"
                  :title="gfTip" @click="openGfPanel(gfBad)" :aria-label="gfAriaLabel">
            <i class="gf-presence-dot" aria-hidden="true"></i>
            <span class="gf-presence-text">{{ gfText }}</span>
          </button>
          <button class="topbar-ops" :class="{ active: view === 'operations' }" @click="switchView('operations')" aria-label="打开经营中心" title="经营中心">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/></svg>
            <span>经营</span>
          </button>
          <button class="topbar-cmd" @mouseenter="prefetchPanel('commandPalette')" @focus="prefetchPanel('commandPalette')" @click="cmdOpen = true" title="命令面板 (Ctrl / ⌘ + K)" aria-label="打开命令面板，⌘K">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><line x1="16.5" y1="16.5" x2="21" y2="21"/></svg>
            <kbd>⌘K</kbd>
          </button>
          <button class="btn btn-primary topbar-add" @mouseenter="prefetchPanel('recordModal')" @focus="prefetchPanel('recordModal')" @click="openAdd">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
            <span>上传截图</span>
          </button>
        </div>
      </header>

      <Transition name="syncbar">
        <div v-if="syncState !== 'ok'" class="sync-bar" :class="syncState" role="status" aria-live="polite">
          <span class="sync-ico" aria-hidden="true">
            <svg v-if="syncState === 'down'" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 8.8a15 15 0 0 1 20 0"/><path d="M5.5 12.5a10 10 0 0 1 13 0"/><path d="M9 16.2a5 5 0 0 1 6 0"/><line x1="3" y1="3" x2="21" y2="21"/></svg>
            <svg v-else width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M2 8.8a15 15 0 0 1 20 0"/><path d="M5.5 12.5a10 10 0 0 1 13 0"/><path d="M9 16.2a5 5 0 0 1 6 0"/><line x1="12" y1="20" x2="12.01" y2="20"/></svg>
          </span>
          <span class="sync-text">{{ syncState === 'down' ? '实时同步已断开，正在重连…' : '已重新连接' }}</span>
          <span v-if="syncState === 'down'" class="sync-hint">这期间的改动不会自动刷新，恢复后会补上</span>
        </div>
      </Transition>

      <div class="main-body">
        <!-- 不做 leave/enter 排队：切视图（含切店重建）时 DOM 立即替换，
             入场动画交给 .page 的 CSS animation 重放。
             旧写法用 <Transition mode="out-in">，只要 leave 的 transitionend
             因页面切到后台等原因丢失，新视图就永远等不到插入时机，整页空白。 -->
        <!-- 保留缓存：切回某个页面时先用上次的内容渲染，再后台静默刷新。
             以前每次切页都重新挂载 → 先出整页骨架再等接口，看起来「一直在加载」。 -->
        <KeepAlive :max="7">
          <Overview v-if="view === 'overview'" :key="'ov-' + activeShopId" :refreshKey="refreshKey" @add="openAdd" @goto-records="gotoRecords" @goto-record="gotoRecord" @goto-product="gotoProduct" @goto-settings="switchView('settings')" @goto-calc="calcOpen = true" @goto-report="reportOpen = true" @notify="notify" />
          <Records v-else-if="view === 'records'" :key="'rc-' + activeShopId" :refreshKey="refreshKey" :highlightId="highlightId" :focusQuery="focusQuery" :focusNorm="focusNorm" :focusType="focusType" :focusNonce="focusNonce" @add="openAdd" @edit="openEdit" @refresh="bump" @notify="notify" @goto-group="gotoGroup" @goto-consign="gotoConsign" @batch="openBatch" />
          <Groups v-else-if="view === 'groups'" :key="'gp-' + activeShopId" :refreshKey="refreshKey" :focusGroupId="focusGroupId" @notify="notify" @edit="openEdit" @view="openViewRecord" @focused="focusGroupId = null" />
          <Consign v-else-if="view === 'consign'" :key="'cs-' + activeShopId" :refreshKey="refreshKey" :initialTab="consignTab" :focusNonce="consignFocusNonce" @notify="notify" />
          <Operations v-else-if="view === 'operations'" :key="'op-' + activeShopId" :refreshKey="refreshKey" :globalAdmin="globalSession" :initialTab="operationsTab" @notify="notify" @refresh="bump" @goto-records="gotoRecords" @goto-record="gotoRecord" @goto-group="gotoGroup" @goto-consign="gotoConsign" @goto-settings="gotoSettings" @use-price="openAdd" />
          <Settings v-else :key="'st-' + activeShopId" :refreshKey="refreshKey" :focusPanel="settingsFocusPanel" :focusNonce="settingsFocusNonce" @notify="notify" @refresh="bump" @open-report="reportOpen = true" @goto-operations="gotoOperations" />
        </KeepAlive>
        <SiteFooter />
      </div>
    </main>

    <!-- 下拉刷新指示器 -->
    <div class="pull-hint" :class="{ show: pullDist > 0, busy: pullBusy }" :style="{ '--pull': pullDist + 'px' }" aria-hidden="true">
      <span v-if="pullBusy" class="spin"></span>
      <svg v-else class="pull-arrow" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14"/><path d="M6 13l6 6 6-6"/></svg>
    </div>

    <Transition name="to-top">
      <button v-if="showToTop" class="to-top" @click="toTop" aria-label="回到顶部" title="回到顶部">
        <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V5"/><path d="M6 11l6-6 6 6"/></svg>
      </button>
    </Transition>

    <!-- 手机底部导航 -->
    <nav class="tabbar" :class="{ 'is-input-focus': inputFocused }">
      <button v-for="n in mobileNavItems" :key="n.key" :class="{ active: view === n.key }" :aria-current="view === n.key ? 'page' : undefined"
              @touchstart.passive="prefetchView(n.key)" @focus="prefetchView(n.key)" @click="switchView(n.key)">
        <span class="tab-ico">
          <svg width="21" height="21" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path :d="n.icon" /></svg>
          <i v-if="n.key === 'records' && todoCount > 0" class="tab-badge" :title="todoCount + ' 单未挂成本'">{{ todoCount > 99 ? '99+' : todoCount }}</i>
        </span>
        <span>{{ n.label }}</span>
      </button>
      <button class="tab-fab" @touchstart.passive="prefetchPanel('recordModal')" @focus="prefetchPanel('recordModal')" @click="openAdd" aria-label="上传截图录入">
        <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
      </button>
    </nav>

    <Transition name="install">
      <div v-if="showInstall" class="install-bar" role="dialog" aria-label="安装应用">
        <span class="install-ico" aria-hidden="true">
          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="5" y="2" width="14" height="20" rx="3"/><line x1="12" y1="18" x2="12.01" y2="18"/></svg>
        </span>
        <div class="install-text"><b>安装到桌面</b><small>像 App 一样打开，离线也能用</small></div>
        <button class="btn btn-sm btn-primary" @click="doInstall">安装</button>
        <button class="btn btn-sm btn-quiet" @click="dismissInstall">以后</button>
      </div>
    </Transition>

    <Transition name="modal" @after-leave="clearEditing">
      <RecordModal v-if="showModal" :record="editing" :viewOnly="viewOnly" :prefill="calcPrefill" :initialFile="sharedFile" @close="closeModal" @saved="onSaved" @open-settings="goSettingsFromModal" />
    </Transition>
    <Transition name="modal">
      <component :is="BatchImportComp" v-if="batchOpen && BatchImportComp"
                 @close="batchOpen = false" @done="onBatchDone" />
    </Transition>
    <!-- toast 也传送到 body：版本窗口等同级弹层会抬高到 body 根层，
         留在 app-shell 的层叠上下文内会被遮住。 -->
    <Teleport to="body">
      <TransitionGroup name="toast" tag="div" class="toast-stack" role="status" aria-live="polite">
        <div v-for="t in toasts" :key="t.id" class="toast" :class="{ err: t.err, warn: t.tone === 'warn', info: t.tone === 'info' }" :style="{ '--toast-ttl': t.ttl + 'ms' }">
          <span class="toast-ico" aria-hidden="true">
            <svg v-if="t.err" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round"><line x1="12" y1="7" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
            <svg v-else-if="t.tone === 'warn'" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M10.3 3.6 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.6a2 2 0 0 0-3.4 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
            <svg v-else-if="t.tone === 'info'" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><line x1="12" y1="11" x2="12" y2="16"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
            <svg v-else width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12l4.5 4.5L19 7"/></svg>
          </span>
          <span class="toast-text">{{ t.text }}</span>
          <button v-if="t.action" class="toast-action" :disabled="t.busy" @click="runToastAction(t)">
            <svg v-if="t.action.icon === 'undo'" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M9 14 4 9l5-5"/><path d="M4 9h11a5 5 0 0 1 0 10h-3"/></svg>
            {{ t.busy ? '处理中…' : t.action.label }}
          </button>
          <span class="toast-bar" aria-hidden="true"></span>
        </div>
      </TransitionGroup>
    </Teleport>
    <CommandPalette :open="cmdOpen" :commands="commands" @close="cmdOpen = false" @run="(c) => c.run()" />
    <PriceCalc :open="calcOpen" @close="calcOpen = false" @use="onCalcUse" />
    <ReportPanel :open="reportOpen" @close="reportOpen = false" />
    <ConfirmHost />
  </div>
</template>

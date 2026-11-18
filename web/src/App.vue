<script setup>
import { ref, computed, watch, nextTick, onMounted, onUnmounted, defineComponent, shallowRef, h } from 'vue'
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
  settings: () => import('./views/Settings.vue')
}
const Records = lazyView('records', viewLoaders.records)
const Settings = lazyView('settings', viewLoaders.settings)
const Groups = lazyView('groups', viewLoaders.groups)
import RecordModal from './components/RecordModal.vue'
import ConfirmHost from './components/ConfirmHost.vue'
import LoginView from './components/LoginView.vue'
import CommandPalette from './components/CommandPalette.vue'
import PriceCalc from './components/PriceCalc.vue'
import ReportPanel from './components/ReportPanel.vue'
import { api } from './api'
import { currentConfirm, settleConfirm } from './confirm'
import { themeMode } from './theme'
import ShopSwitcher from './components/ShopSwitcher.vue'
import { activeId as activeShopId, loadShops, globalSession, hasAnyPassword } from './shopStore'
import { shopPickedThisSession, clearShopPicked } from './shopId'
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

// 首屏之后趁空闲把其余视图的分包取回来。切店铺时视图会整体重建，
// 分包若还没到就会先显示骨架——预取之后这一步永远是同步的，不会再空一下。
const preloadTargets = Object.entries(viewLoaders)
function preloadViews() {
  if (navigator.onLine === false) return
  const run = () => {
    for (const [k, l] of preloadTargets) {
      try { loadChunk(k, l).catch(() => {}) } catch (e) { /* 预取失败不影响使用 */ }
    }
  }
  if (window.requestIdleCallback) window.requestIdleCallback(run, { timeout: 3000 })
  else setTimeout(run, 1500)
}

// ---------- 多人实时同步 ----------
// 收到「别人」的变更后自动重拉当前视图；自己触发的变更已由本地流程刷新，跳过。
// 批量操作会连续产生多条事件，用 2.5s 节流避免提示刷屏。
let offRealtime = null
let lastSyncToast = 0
// 别人连续改了好几条时，事件会一条条来；每条都触发整页重拉就是白白多拉几次。
// 300ms 内合并成一次刷新，体感完全一样，请求量按事件数下降。
let bumpTimer = null
function bumpCoalesced() {
  if (bumpTimer) clearTimeout(bumpTimer)
  bumpTimer = setTimeout(() => { bumpTimer = null; bump() }, 300)
}
function startRealtime() {
  connectRealtime()
  offRealtime = onRealtime((ev) => {
    if (!ev || ev.kind !== 'change') return
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
  bump()
  loadTodoCount()
  connectRealtime()
})

onMounted(() => {
  slowBootTimer = setTimeout(() => { if (authChecking.value) slowBoot.value = true }, 6000)
  loadTodoCount()
  checkAuth()
  loadShops()
  startRealtime()
  preloadViews()
  window.addEventListener('xy-unauthorized', onUnauthorized)
  window.addEventListener('hashchange', onHash)
  window.addEventListener('keydown', onGlobalKey)
  window.addEventListener('keydown', onShortcut)
  window.addEventListener('xy-update', onUpdate)
  window.addEventListener('beforeinstallprompt', onBeforeInstall)
  window.addEventListener('scroll', onScroll, { passive: true })
  onScroll()
})
onUnmounted(() => {
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
  document.body.classList.remove('overlay-open')
})
function onLoginSuccess() { needLogin.value = false; bump() }
async function doLogout() {
  try { await api.logout() } catch (e) { /* ignore */ }
  clearShopPicked()
  needLogin.value = true
}

const validViews = ['overview', 'records', 'groups', 'settings']
const hashView = (h) => {
  const v = h.replace('#', '').replace('-add', '')
  return validViews.includes(v) ? v : 'overview'
}
const view = ref(hashView(location.hash))
const showModal = ref(location.hash.endsWith('-add'))
const viewOnly = ref(false)          // 弹窗是否以「只读查看」打开
const focusQuery = ref('')           // 从商品利润排行跳明细时要过滤的商品名
const focusNorm = ref('')            // 该商品的归一化名（product_norm），用于精确过滤整组单据
const focusType = ref('')            // 非空 = 跳过去时直接按类型过滤（如 unlinked 未挂成本）
const focusNonce = ref(0)            // 每次跳转自增：明细页挂载时也能收到，且连点同一商品仍会重新过滤
const editing = ref(null)
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
  { key: 'settings', label: '设置', icon: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.6 1.6 0 0 0 .3 1.8l.1.1-2.8 2.8-.1-.1a1.6 1.6 0 0 0-1.8-.3 1.6 1.6 0 0 0-1 1.5V21h-4v-.2a1.6 1.6 0 0 0-1-1.5 1.6 1.6 0 0 0-1.8.3l-.1.1-2.8-2.8.1-.1a1.6 1.6 0 0 0 .3-1.8 1.6 1.6 0 0 0-1.5-1H3v-4h.2a1.6 1.6 0 0 0 1.5-1 1.6 1.6 0 0 0-.3-1.8l-.1-.1 2.8-2.8.1.1a1.6 1.6 0 0 0 1.8.3H9a1.6 1.6 0 0 0 1-1.5V3h4v.2a1.6 1.6 0 0 0 1 1.5 1.6 1.6 0 0 0 1.8-.3l.1-.1 2.8 2.8-.1.1a1.6 1.6 0 0 0-.3 1.8V9a1.6 1.6 0 0 0 1.5 1H21v4h-.2a1.6 1.6 0 0 0-1.4 1z' }
]
const pageTitle = computed(() => ({ overview: '经营概览', records: '交易明细', groups: '联动单', settings: '系统设置' }[view.value]))
const pageSub = computed(() => ({
  overview: '实时掌握利润、成本与费用构成',
  records: '按交易时间排序 · 检索 / 筛选 / 批量管理',
  groups: '多买多卖挂同一个联单 · 看哪类商品利润高',
  settings: '识别引擎、安全、备份与数据'
}[view.value]))

// 命令面板条目：页面跳转 + 常用动作 + 主题切换
const commands = computed(() => [
  { id: 'nav-overview', label: '经营概览', desc: '利润、成本与费用构成', icon: navItems[0].icon, hint: '页面', keywords: 'overview 首页 概览', run: () => switchView('overview') },
  { id: 'nav-records', label: '交易明细', desc: '检索 / 筛选 / 批量管理', icon: navItems[1].icon, hint: '页面', keywords: 'records 明细 记录 列表', run: () => switchView('records') },
  { id: 'nav-groups', label: '联动单', desc: '多买多卖挂同一联单', icon: navItems[2].icon, hint: '页面', keywords: 'groups 联单 联动', run: () => switchView('groups') },
  { id: 'nav-settings', label: '系统设置', desc: '识别引擎、安全、备份与数据', icon: navItems[3].icon, hint: '页面', keywords: 'settings 设置 配置 备份', run: () => switchView('settings') },
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
  const ttl = action?.ttl || (action ? 9000 : 3200)
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
function openAdd(prefill = null) {
  // @click="openAdd" 会把 MouseEvent 传进来，不能当成预填数据
  const pf = prefill && typeof prefill === 'object' && !(prefill instanceof Event) && 'sell_price' in prefill ? prefill : null
  editing.value = null
  calcPrefill.value = pf
  showModal.value = true
  setHash(hashFor(view.value, true))
}
// 定价速算「用这个价录入」：关掉工具，带着结果打开录入弹窗
function onCalcUse(data) { openAdd(data) }
function openEdit(r) { viewOnly.value = false; editing.value = r; showModal.value = true }
// 联单里点订单：默认「查看」，要看腻了再点弹窗里的「编辑」。
// 和明细/联单页显式的编辑按钮区分开，避免手一滑就改到账。
function openViewRecord(r) { viewOnly.value = true; editing.value = r; showModal.value = true }
// 关闭时先让 Transition 走完离场动画，再清空编辑数据，避免淡出过程中表单闪成空白
function closeModal() { showModal.value = false; viewOnly.value = false; calcPrefill.value = null; setHash(hashFor(view.value)) }
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
  // 在明细页保存时，把这条记录标记出来：列表会整体重拉，不标一下根本找不到刚加的那条。
  // 在概览页保存就不跳转了——用户没在看清明，硬切页面反而打断。
  if (view.value === 'records' && info?.id) highlightId.value = info.id
  bump()
  if (info?.merged) notify(`已自动合并到同名商品「${info.product}」`)
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
        <button v-for="n in navItems" :key="n.key" :class="{ active: view === n.key }" @click="switchView(n.key)">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path :d="n.icon" /></svg>
          <span>{{ n.label }}</span>
        </button>
      </nav>
      <div class="side-foot">
        <button class="side-add" @click="openAdd">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
          上传截图录入
        </button>
        <button v-if="authEnabled" class="side-logout" @click="doLogout">退出登录</button>
        <div class="side-ver muted">数据仅存本机 · 离线可用</div>
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
          <ShopSwitcher @notify="notify" />
          <span class="presence" :class="{ live: connected }"
                :title="connected ? `实时同步已连接 · ${online} 个客户端在线` : '实时同步未连接，正在自动重连…'">
            <i class="presence-dot" aria-hidden="true"></i>
            <span class="presence-text">{{ connected ? (online > 1 ? `${online} 人在线` : '实时同步') : '离线' }}</span>
          </span>
          <button class="topbar-cmd" @click="cmdOpen = true" title="命令面板 (Ctrl / ⌘ + K)" aria-label="打开命令面板">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><line x1="16.5" y1="16.5" x2="21" y2="21"/></svg>
            <kbd>⌘K</kbd>
          </button>
          <button class="btn btn-primary topbar-add" @click="openAdd">
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
        <Overview v-if="view === 'overview'" :key="'ov-' + activeShopId" :refreshKey="refreshKey" @add="openAdd" @goto-records="gotoRecords" @goto-record="gotoRecord" @goto-product="gotoProduct" @goto-settings="switchView('settings')" @goto-calc="calcOpen = true" @goto-report="reportOpen = true" />
        <Records v-else-if="view === 'records'" :key="'rc-' + activeShopId" :refreshKey="refreshKey" :highlightId="highlightId" :focusQuery="focusQuery" :focusNorm="focusNorm" :focusType="focusType" :focusNonce="focusNonce" @add="openAdd" @edit="openEdit" @refresh="bump" @notify="notify" @goto-group="gotoGroup" @batch="openBatch" />
        <Groups v-else-if="view === 'groups'" :key="'gp-' + activeShopId" :refreshKey="refreshKey" :focusGroupId="focusGroupId" @notify="notify" @edit="openEdit" @view="openViewRecord" @focused="focusGroupId = null" />
        <Settings v-else :key="'st-' + activeShopId" :refreshKey="refreshKey" @notify="notify" @refresh="bump" />
      </div>
    </main>

    <Transition name="to-top">
      <button v-if="showToTop" class="to-top" @click="toTop" aria-label="回到顶部" title="回到顶部">
        <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V5"/><path d="M6 11l6-6 6 6"/></svg>
      </button>
    </Transition>

    <!-- 手机底部导航 -->
    <nav class="tabbar">
      <button v-for="n in navItems" :key="n.key" :class="{ active: view === n.key }" @click="switchView(n.key)">
        <span class="tab-ico">
          <svg width="21" height="21" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path :d="n.icon" /></svg>
          <i v-if="n.key === 'records' && todoCount > 0" class="tab-badge" :title="todoCount + ' 单未挂成本'">{{ todoCount > 99 ? '99+' : todoCount }}</i>
        </span>
        <span>{{ n.label }}</span>
      </button>
      <button class="tab-fab" @click="openAdd" aria-label="上传截图录入">
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
      <RecordModal v-if="showModal" :record="editing" :viewOnly="viewOnly" :prefill="calcPrefill" @close="closeModal" @saved="onSaved" @open-settings="goSettingsFromModal" />
    </Transition>
    <Transition name="modal">
      <component :is="BatchImportComp" v-if="batchOpen && BatchImportComp"
                 @close="batchOpen = false" @done="onBatchDone" />
    </Transition>
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
    <CommandPalette :open="cmdOpen" :commands="commands" @close="cmdOpen = false" @run="(c) => c.run()" />
    <PriceCalc :open="calcOpen" @close="calcOpen = false" @use="onCalcUse" />
    <ReportPanel :open="reportOpen" @close="reportOpen = false" />
    <ConfirmHost />
  </div>
</template>

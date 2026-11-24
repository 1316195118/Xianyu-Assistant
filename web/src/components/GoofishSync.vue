<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { shops, activeId, loadShops } from '../shopStore'
import HelpTip from './HelpTip.vue'
import { maskCloser } from '../maskClose'
import { gfStatus, gfRebindRequest } from '../gfState'
import { onRealtime } from '../realtime'
import { confirm } from '../confirm'
import QRCode from 'qrcode'
// 遮罩点击守卫：在输入框里拖选文字划出弹窗松手时，不会误关弹窗
const mask = maskCloser(() => emit('close'))


const emit = defineEmits(['close', 'notify', 'refresh'])
// ---------- 扫码登录 ----------
const qrOpen = ref(false)
const qrImg = ref('')
const qrStatus = ref('')   // new / scanned / confirmed / failed
const qrMsg = ref('')
let qrTimer = null
async function startQR() {
  clearInterval(qrTimer)  // 防重复点击产生双轮询
  qrOpen.value = true; qrStatus.value = 'new'; qrMsg.value = ''
  try {
    const r = await api.goofishQRStart()
    qrImg.value = await QRCode.toDataURL(r.code_content, { width: 230, margin: 1 })
    // 轮询：每 2.5 秒一次，最多 5 分钟
    clearInterval(qrTimer)
    let tries = 0
    qrTimer = setInterval(async () => {
      tries++
      if (tries > 120) { clearInterval(qrTimer); qrStatus.value = 'failed'; qrMsg.value = '二维码已过期，请重新生成'; return }
      try {
        const s = await api.goofishQRPoll()
        qrStatus.value = s.status
        if (s.status === 'scanned') qrMsg.value = '已扫码，请在手机上确认'
        if (s.status === 'confirmed') {
          clearInterval(qrTimer)
          qrMsg.value = '登录成功：' + (s.nick || '')
          emit('refresh')
          setTimeout(() => { qrOpen.value = false }, 1800)
        }
        if (s.status === 'failed') {
          clearInterval(qrTimer)
          qrMsg.value = s.error || '登录失败'
        }
      } catch (e) { /* 单次轮询失败忽略，下次再试 */ }
    }, 2500)
  } catch (e) {
    qrStatus.value = 'failed'
    qrMsg.value = '二维码生成失败：' + (e.message || '未知错误')
  }
}
function stopQR() {
  clearInterval(qrTimer)
  qrOpen.value = false
  qrImg.value = ''
}
let statusTimer = null
onUnmounted(() => {
  stopQR()
  clearInterval(statusTimer)
  clearReviewTimer()
  if (offProgress) offProgress()
  document.removeEventListener('keydown', onKey)
})

// step: bind（未绑定）/ idle（已绑定）/ loading / review / done
const step = ref('idle')
// 重新绑定：保留当前账号，只是换一份凭证，不用先解绑
const rebinding = ref(false)
const progress = ref({ on: false, pct: 0, text: '' })
let offProgress = null
const status = ref({
  logged_in: false, credential_saved: false, session_valid: false, order_api_valid: false,
  nick: '', saved_at: '', last_sync: '', last_check: '', masked: '', keepalive: true,
  expired: false, auto_days: 0, auto_window: 30, backfill: true, syncing: false,
  sso_saved: false, retry_at: '', bridge_online: false, bridge_last_seen: '',
  bridge_token_configured: false, bridge_auth_required: false
})
const cookieInput = ref('')
const showHelp = ref(false)
const pingMsg = ref('')
// 是否按账单补正「已在库」记录的金额。默认关：后台和普通同步都不动已有金额。
// 注意：联单关联、完结、弃单状态任何时候都不会被同步改动。
const allowOverwrite = ref(false)
// 逐条勾选要补正的订单：预览表里每条已有卖单都能单独选择
const refreshPicked = ref({})
const plan = ref(null)
// 历史补建：列表接口只回最近 40 单，这里可以按账单期往前补
const backfillMonths = ref(12)
// 手动同步时是否一并做账单回溯。值由后端按店铺持久化（status.backfill），
// 刷新/退出再进都会保持上次的选择；默认开（沿用原行为）。
const useBackfill = ref(true)
const picked = ref({})
const err = ref('')
const busy = ref(false)
// 立即同步走后台自动规则；只有手动同步才生成订单清单。
const syncMode = ref('auto') // auto | manual
// 「检测连接」单独一个 busy：它和「立即同步」并存，共用一个会把另一个按钮也带上假的「检测中」
const pingBusy = ref(false)
const result = ref(null)
const bridgeToken = ref('')
const bridgeTokenBusy = ref(false)
const bridgeTokenCopied = ref(false)
const MANUAL_REVIEW_TIMEOUT_MS = 2 * 60 * 60 * 1000
const reviewDeadline = ref(0)
const reviewRemaining = ref(0)
let reviewTimer = null

const rows = computed(() => (plan.value && plan.value.rows) || [])
const stat = computed(() => (plan.value && plan.value.stat) || {})

const importable = (r) => !r.skip_reason && !r.exists
// 已在库且金额确实不同的卖单才需要补正：已出账核对实收/服务费，未出账只核对成交价。
const willOverwrite = (r) => r.exists && r.refresh_needed && !r.skip_reason && r.side === 'sell'
const willKeep = (r) => r.exists && r.billed && r.side === 'sell' && !r.managed
const pickedList = computed(() => rows.value.filter((r) => importable(r) && picked.value[r.order_no + '|' + r.side]))
// 已在库、但这次拿到了真实扣费的卖单：点一次「导入」会顺手把它们的服务费与到手价补正
const refreshable = computed(() => rows.value.filter(willOverwrite).length)
const refreshCount = computed(() => rows.value.filter((r) => willOverwrite(r) && refreshPicked.value[r.order_no]).length)
const keptManual = computed(() => 0) // 现在按账单覆盖所有已在库卖单，不再有「手动保留」
const pickedCount = computed(() => pickedList.value.length)
const doneTitle = computed(() => {
  const added = Number(result.value?.added || 0)
  const refreshed = Number(result.value?.refreshed || 0)
  if (syncMode.value === 'auto') {
    if (added > 0 && refreshed > 0) return `后台同步完成：新增 ${added} 条、补正 ${refreshed} 条`
    if (added > 0) return `后台同步完成：新增 ${added} 条`
    if (refreshed > 0) return `后台同步完成：补正 ${refreshed} 条卖单`
    return '后台同步完成，没有需要变更的订单'
  }
  if (added > 0) return `已导入 ${added} 条`
  if (refreshed > 0) return `已补正 ${refreshed} 条卖单`
  return '同步已核对，无需改动'
})
const reviewRemainingLabel = computed(() => {
  const sec = Math.max(0, Math.ceil(reviewRemaining.value / 1000))
  return `${String(Math.floor(sec / 60)).padStart(2, '0')}:${String(sec % 60).padStart(2, '0')}`
})
const loadingText = computed(() => syncMode.value === 'manual'
  ? '正在拉取订单与账单并生成手动同步清单…'
  : `正在后台同步最近 ${status.value.auto_window || 30} 天的订单…`)
const allPicked = computed({
  get: () => {
    const list = rows.value.filter(importable)
    return list.length > 0 && list.every((r) => picked.value[r.order_no + '|' + r.side])
  },
  set: (v) => {
    const next = { ...picked.value }
    for (const r of rows.value) {
      if (importable(r)) next[r.order_no + '|' + r.side] = v
    }
    picked.value = next
  }
})

function selectRefreshable() {
  const next = {}
  for (const r of rows.value) {
    if (willOverwrite(r)) next[r.order_no] = true
  }
  refreshPicked.value = next
}

watch(allowOverwrite, (on) => {
  if (on) selectRefreshable()
  else refreshPicked.value = {}
})

const shopName = computed(() => status.value.shop_name || (shops.value.find((x) => x.id === activeId.value) || {}).name || '当前店铺')

const money = (v) => {
  const n = Number(v) || 0
  return n.toFixed(2).replace(/\.00$/, '')
}
const dayOf = (t) => String(t || '').slice(0, 10) || '—'
const bridgeLabel = computed(() => status.value.bridge_online ? '在线' : (status.value.bridge_last_seen ? '已离线' : '未连接'))
const bridgeDetail = computed(() => status.value.bridge_online
  ? '后台自动续期，登录态变化后自动上报'
  : (status.value.bridge_last_seen
    ? '浏览器已关闭或扩展未运行；服务器仍可用已保存凭证拉单'
    : '安装扩展后自动连接，无需托盘或专用窗口'))
const orderApiLabel = computed(() => {
  if (status.value.order_api_valid) return '可用'
  if (status.value.sync_error || status.value.expired) return '已失效'
  if (status.value.last_check) return '待复核'
  return '待检测'
})

async function loadStatus() {
  try {
    const s = await api.goofishStatus()
    status.value = s
    // 同步给顶栏常驻状态点：别的页面也能看到失效，不用先进设置页
    gfStatus.value = {
      logged_in: !!s.logged_in,
      session_valid: !!s.session_valid,
      order_api_valid: !!s.order_api_valid,
      expired: !!s.expired,
      sync_error: s.sync_error || '',
      nick: s.nick || '',
      bridge_online: !!s.bridge_online
    }
    // 恢复上次保存的账单回溯开关（后端按店铺持久化）
    useBackfill.value = s.backfill !== false
    step.value = s.logged_in ? 'idle' : 'bind'
    pingMsg.value = ''
    // 自动同步失败会存 sync_error，页面一打开就能看到具体原因（不用等手动同步）
    const bridgeHint = s.bridge_online
      ? '浏览器扩展在线，请点击扩展图标并使用「打开官方扫码登录」，成功后会立即自动上报。'
      : '请在当前浏览器登录闲鱼并确认扩展已启用，或临时重新粘贴 Cookie。'
    if (s.sync_error) pingMsg.value = s.sync_error + '。' + bridgeHint
    else if (s.expired) pingMsg.value = '登录会话已失效，服务器无法自行换回 cookie2。' + bridgeHint
  } catch (e) {
    step.value = 'bind'
    err.value = e.message || '状态读取失败'
  }
}

async function issueBridgeToken() {
  if (bridgeTokenBusy.value) return
  bridgeTokenBusy.value = true
  bridgeTokenCopied.value = false
  try {
    const r = await api.goofishBridgeToken()
    bridgeToken.value = r.token || ''
    status.value = { ...status.value, bridge_token_configured: true }
    emit('notify', '扩展配对码已生成，请复制到扩展快捷面板；旧配对码已失效')
  } catch (e) {
    emit('notify', e.message || '生成扩展配对码失败', true)
  } finally {
    bridgeTokenBusy.value = false
  }
}

async function copyBridgeToken() {
  if (!bridgeToken.value) return
  try {
    await navigator.clipboard.writeText(bridgeToken.value)
    bridgeTokenCopied.value = true
    setTimeout(() => { bridgeTokenCopied.value = false }, 1800)
  } catch (e) {
    emit('notify', '浏览器未允许自动复制，请手动选中配对码复制', true)
  }
}

async function revokeBridgeToken() {
  if (bridgeTokenBusy.value) return
  const ok = await confirm({
    title: '撤销扩展配对码',
    message: '撤销后，已安装的浏览器扩展会停止自动上报，直到重新生成并填入新配对码。',
    confirmText: '撤销', danger: true
  })
  if (!ok) return
  bridgeTokenBusy.value = true
  try {
    await api.revokeGoofishBridgeToken()
    bridgeToken.value = ''
    status.value = { ...status.value, bridge_token_configured: false }
    emit('notify', '扩展配对码已撤销')
  } catch (e) {
    emit('notify', e.message || '撤销扩展配对码失败', true)
  } finally {
    bridgeTokenBusy.value = false
  }
}

async function bind() {
  const raw = cookieInput.value.trim()
  if (!raw || busy.value) return
  busy.value = true
  err.value = ''
  try {
    const r = await api.goofishLogin(raw)
    emit('notify', `已${rebinding.value ? '重新' : ''}绑定闲鱼账号「${r.nick}」`)
    cookieInput.value = ''
    rebinding.value = false
    await loadStatus()
  } catch (e) {
    err.value = e.message || '绑定失败'
    emit('notify', err.value, true)
  } finally {
    busy.value = false
  }
}

async function unbind() {
  if (busy.value) return
  const ok = await confirm({
    title: '解绑闲鱼账号',
    message: '解绑后自动同步和订单拉取都会停止，需要重新粘贴 Cookie 或扫码才能恢复。',
    confirmText: '解绑', danger: true
  })
  if (!ok) return
  busy.value = true
  try {
    await api.goofishLogout()
    plan.value = null
    picked.value = {}
    emit('notify', '已解绑闲鱼账号')
    await loadStatus()
  } catch (e) {
    err.value = e.message || '解绑失败'
    emit('notify', err.value, true)
  } finally {
    busy.value = false
  }
}

async function toggleKeepAlive() {
  const on = !status.value.keepalive
  try {
    await api.goofishKeepAlive(on)
    status.value = { ...status.value, keepalive: on }
    emit('notify', on ? '已开启后台登录检查' : '已关闭后台登录检查')
  } catch (e) {
    emit('notify', e.message || '设置失败', true)
  }
}

const autoOptions = [
  { v: 0, t: '关闭自动同步' }, { v: 1, t: '每天一次' }, { v: 3, t: '每 3 天' },
  { v: 7, t: '每 7 天' }, { v: 15, t: '每 15 天' }, { v: 30, t: '每 30 天' }
]
async function changeAuto(e) {
  const days = Number(e.target.value)
  try {
    await api.goofishAuto(days, status.value.auto_window || 0)
    status.value = { ...status.value, auto_days: days }
    emit('notify', days ? `已设为每 ${days} 天自动同步一次` : '已关闭自动同步')
  } catch (err) {
    emit('notify', err.message || '设置失败', true)
  }
}

async function changeWindow(e) {
  const w = Number(e.target.value)
  try {
    const r = await api.goofishAuto(status.value.auto_days || 0, w)
    status.value = { ...status.value, auto_window: r.window || w }
    emit('notify', `最近 ${r.window || w} 天：立即同步和后台自动同步都只处理这个范围`)
  } catch (err) {
    emit('notify', err.message || '设置失败', true)
  }
}

// 账单回溯开关：存后端按店铺持久化，退出再进保持上次选择
async function changeBackfill() {
  const on = useBackfill.value
  try {
    await api.goofishAuto(status.value.auto_days || 0, status.value.auto_window || 0, on)
    emit('notify', on
      ? '手动同步将一并做账单回溯；立即同步始终只处理最近 N 天'
      : '手动同步不再做账单回溯；立即同步始终只处理最近 N 天')
  } catch (err) {
    useBackfill.value = !on // 保存失败则回滚勾选状态
    emit('notify', err.message || '设置失败', true)
  }
}

async function ping() {
  if (busy.value || pingBusy.value) return
  pingBusy.value = true
  pingMsg.value = ''
  try {
    const r = await api.goofishPing()
    // 必须先刷新状态再写提示：loadStatus 里会清空 pingMsg，
    // 顺序反了会把刚测出来的「连接正常」立刻抹掉，看起来就像没反应。
    await loadStatus()
    const msg = `连接正常，当前账号「${r.nick || status.value.nick || '闲鱼账号'}」`
    emit('notify', msg)
    // 如果同步本身有失效原因，别让一句「连接正常」把它盖住
    if (!status.value.sync_error && !status.value.expired) pingMsg.value = msg
  } catch (e) {
    // 检测失败后同步刷新失效标记与自动续期开关，避免面板状态落后。
    await loadStatus()
    const msg = e.message || '连接已失效'
    pingMsg.value = msg
    emit('notify', msg, true)
  } finally {
    pingBusy.value = false
  }
}

async function pull() {
  if (busy.value) return
  busy.value = true
  syncMode.value = 'manual'
  clearReviewTimer()
  err.value = ''
  step.value = 'loading'
  progress.value = { on: true, pct: 2, text: '准备中…' }
  try {
    const months = useBackfill.value ? backfillMonths.value : 0
    const p = await api.goofishPreview(0, months)
    plan.value = p
    const next = {}
    for (const r of p.rows) {
      if (!r.skip_reason && !r.exists) next[r.order_no + '|' + r.side] = true
    }
    picked.value = next
    if (allowOverwrite.value) selectRefreshable()
    else refreshPicked.value = {}
    progress.value = { on: true, pct: 100, text: '完成' }
    step.value = 'review'
    startReviewTimer(p.made_at)
  } catch (e) {
    progress.value = { on: false, pct: 0, text: '' }
    err.value = e.message || '拉取失败'
    step.value = 'idle'
    // 令牌失效 / 被风控 / 网络不通，都在这里弹出来，避免只看到一句红字
    emit('notify', err.value, true)
  } finally {
    busy.value = false
  }
}

async function syncNow() {
  if (busy.value) return
  busy.value = true
  syncMode.value = 'auto'
  clearReviewTimer()
  err.value = ''
  plan.value = null
  picked.value = {}
  refreshPicked.value = {}
  result.value = null
  step.value = 'loading'
  progress.value = { on: true, pct: 2, text: '准备同步…' }
  try {
    const r = await api.goofishSyncNow()
    await loadStatus()
    result.value = r
    progress.value = { on: true, pct: 100, text: '同步完成' }
    step.value = 'done'
    emit('refresh')
    emit('notify', r.message || `最近 ${r.window || status.value.auto_window || 30} 天同步完成`)
  } catch (e) {
    progress.value = { on: false, pct: 0, text: '' }
    err.value = e.message || '同步失败'
    step.value = 'idle'
    emit('notify', err.value, true)
  } finally {
    busy.value = false
  }
}

async function apply() {
  const canRefresh = allowOverwrite.value && refreshCount.value > 0
  if (busy.value || (!pickedCount.value && !canRefresh)) return
  busy.value = true
  err.value = ''
  try {
    const r = await api.goofishApply({
      plan_id: plan.value.plan_id,
      order_nos: pickedList.value.map((x) => x.order_no),
      refresh: allowOverwrite.value,
      refresh_order_nos: Object.keys(refreshPicked.value).filter((k) => refreshPicked.value[k])
    })
    result.value = r
    clearReviewTimer()
    step.value = 'done'
    emit('refresh')
    if (r.added) emit('notify', `已导入 ${r.added} 条到「${shopName.value}」`)
    if (r.refreshed) emit('notify', `已按最新闲鱼数据补正 ${r.refreshed} 条卖单`)
    if (!r.added && !r.refreshed && r.message) emit('notify', r.message)
  } catch (e) {
    err.value = e.message || '导入失败'
  } finally {
    busy.value = false
  }
}

function clearReviewTimer() {
  if (reviewTimer) {
    clearInterval(reviewTimer)
    reviewTimer = null
  }
  reviewDeadline.value = 0
  reviewRemaining.value = 0
}

function tickReviewTimer() {
  if (!reviewDeadline.value) return
  reviewRemaining.value = Math.max(0, reviewDeadline.value - Date.now())
  if (reviewRemaining.value > 0 || step.value !== 'review' || busy.value) return
  clearReviewTimer()
  backToIdle()
  emit('notify', '手动同步清单已超过 2 小时，已自动返回；需要继续筛选时请重新点击手动同步')
}

function startReviewTimer(madeAt) {
  const made = Date.parse(madeAt || '')
  const base = Number.isFinite(made) ? made : Date.now()
  reviewDeadline.value = base + MANUAL_REVIEW_TIMEOUT_MS
  tickReviewTimer()
  if (reviewRemaining.value <= 0 || step.value !== 'review') return
  reviewTimer = setInterval(tickReviewTimer, 1000)
}

function backToIdle() {
  clearReviewTimer()
  plan.value = null
  picked.value = {}
  refreshPicked.value = {}
  allowOverwrite.value = false
  result.value = null
  err.value = ''
  step.value = status.value.logged_in ? 'idle' : 'bind'
}

// 从顶栏「去绑定」或失效提示进来时，直接打开重新绑定表单
function startRebind() {
  rebinding.value = true
  step.value = 'bind'
  err.value = ''
  cookieInput.value = ''
  stopQR()
}
function cancelRebind() {
  rebinding.value = false
  step.value = 'idle'
  err.value = ''
}
watch(gfRebindRequest, (v) => {
  if (!v) return
  gfRebindRequest.value = false
  startRebind()
})
const onKey = (e) => { if (e.key === 'Escape' && !qrOpen.value) emit('close') }
onMounted(async () => {
  document.addEventListener('keydown', onKey)
  offProgress = onRealtime((ev) => {
    if (!ev) return
    if (ev.shop_id && activeId.value && ev.shop_id !== activeId.value) return
    if (ev.kind === 'change' && ev.path === '/api/goofish/status') {
      loadStatus()
      return
    }
    if (ev.kind !== 'progress') return
    progress.value = { on: true, pct: Number(ev.progress) || 0, text: ev.summary || '' }
  })
  if (gfRebindRequest.value) {
    gfRebindRequest.value = false
    startRebind()
  }
  if (!shops.value.length) {
    try { await loadShops() } catch (e) { /* 忽略：店铺加载失败不影响主流程 */ }
  }
  if (!rebinding.value) await loadStatus()
  statusTimer = setInterval(() => {
    if (!rebinding.value && !busy.value) loadStatus()
  }, 30000)
})
</script>

<template>
  <Teleport to="body">
    <div class="modal-mask" @pointerdown="mask.onPointerDown" @click="mask.onClick">
      <div class="modal gf-modal">
        <div class="modal-head">
          <h3>闲鱼同步</h3>
          <button class="x" @click="emit('close')" aria-label="关闭">×</button>
        </div>

        <div class="modal-body">
          <!-- 未绑定 -->
          <template v-if="step === 'bind'">
            <p class="muted gf-desc">
              <template v-if="rebinding">正在为「{{ shopName }}」重新绑定：新的凭证会直接覆盖旧的，不用先解绑。若当前账号还能登录，继续往下填即可。</template>
              <template v-else>正在为「{{ shopName }}」绑定。绑定后服务端自己拉订单和账单；安装扩展后还会在浏览器登录态变化时自动更新凭证。</template>
            </p>

            <div class="gf-bridge-callout">
              <div>
                <b>正式登录入口：浏览器扩展官方扫码登录</b>
                <span>安装并连接扩展后，点击浏览器工具栏的鱼助手图标，再点「打开官方扫码登录」。扩展会打开闲鱼官方登录页，扫码完成后自动检测、上报并校验。</span>
              </div>
              <div class="gf-extension-install">
                <a class="btn btn-sm btn-primary" href="/downloads/goofish-extension.zip" download="goofish-extension.zip">
                  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/></svg>
                  下载扩展包
                </a>
                <span>下载后解压，运行 <code>install_goofish_extension.bat</code>，再在扩展页加载解压目录。</span>
              </div>
              <small>登录始终由闲鱼官方页面完成，扩展不会保存闲鱼账号密码。电脑关机后服务器仍能用已保存凭证拉单。</small>
            </div>

            <div v-if="status.bridge_auth_required" class="gf-extension-gate">
              <div>
                <b>扩展需要配对码</b>
                <span>当前鱼助手开启了访问密码；生成后填入扩展，旧配对码会立即失效。</span>
              </div>
              <button class="btn btn-sm" :disabled="bridgeTokenBusy" @click="issueBridgeToken">
                <span v-if="bridgeTokenBusy" class="spin"></span>{{ status.bridge_token_configured ? '重新生成' : '生成配对码' }}
              </button>
              <div v-if="bridgeToken" class="gf-extension-token">
                <code>{{ bridgeToken }}</code>
                <button class="btn btn-sm" @click="copyBridgeToken">{{ bridgeTokenCopied ? '已复制' : '复制' }}</button>
              </div>
            </div>

            <div class="gf-manual-title">备用：手动粘贴 Cookie</div>
            <div class="gf-steps">
              <div class="gf-step"><b>1</b><span>电脑浏览器打开 <code>www.goofish.com</code> 并登录</span></div>
              <div class="gf-step"><b>2</b><span>按 <kbd>F12</kbd>，切到「网络」面板（Network）</span></div>
              <div class="gf-step"><b>3</b><span>随便点一下「我买到的」，找到任意 <code>mtop</code> 请求</span></div>
              <div class="gf-step"><b>4</b><span>在「请求标头」（Request Headers）里找到 <code>Cookie:</code>，整行复制</span></div>
            </div>

            <div class="gf-field">
              <label>粘贴 Cookie<HelpTip text="凭证只存在你自己的服务器数据库里，用来代替你访问闲鱼接口，不会上传到任何第三方。&#10;服务器可以用 SSO 检查短期会话，但无法在闲鱼 cookie2 真正失效后自行重新登录；长期使用建议安装浏览器扩展，由真实浏览器自动更新登录态。" /></label>
              <textarea v-model="cookieInput" class="gf-textarea" rows="5"
                        placeholder="直接粘贴整行，例如：cna=xxx; _m_h5_tk=xxx; cookie2=xxx; unb=xxx; …"></textarea>
            </div>


            <div class="gf-qr-sep"><span>或者</span></div>
            <button class="btn gf-qr-btn" :disabled="busy" @click="startQR">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><path d="M14 14h3v3h-3zM20 20h.01"/></svg>
              服务器扫码（实验性，备用）
            </button>
            <div class="gf-qr-tip muted">服务器生成的二维码可能因闲鱼设备指纹校验而失效。正式入口请使用上方浏览器扩展的「官方扫码登录」。</div>

            <div v-if="qrOpen" class="gf-qr-panel">
              <div class="gf-qr-head">
                <b>用闲鱼 App 扫码登录</b>
                <button class="x" @click="stopQR" aria-label="关闭扫码">×</button>
              </div>
              <img v-if="qrImg" class="gf-qr-img" :src="qrImg" alt="闲鱼官方登录二维码，请使用闲鱼 App 扫码"
                   width="230" height="230" decoding="async" />
              <div v-else class="gf-qr-loading"><span class="spin"></span> 正在生成二维码…</div>
              <div v-if="qrMsg" class="gf-qr-msg" :class="{ ok: qrStatus === 'confirmed' || qrStatus === 'scanned', err: qrStatus === 'failed' }">{{ qrMsg }}</div>
              <div v-else-if="qrStatus === 'new'" class="gf-qr-msg muted">打开手机闲鱼 App → 扫一扫 → 确认登录</div>
            </div>

            <div v-if="err" class="tip err">{{ err }}</div>

            <div class="gf-actions">
              <button class="btn btn-primary" :disabled="busy || !cookieInput.trim()" @click="bind">
                <span v-if="busy" class="spin"></span>{{ busy ? '校验中' : (rebinding ? '重新绑定并校验' : '绑定并校验') }}
              </button>
              <button v-if="rebinding" class="btn btn-ghost" :disabled="busy" @click="cancelRebind">取消，返回</button>
            </div>
          </template>

          <!-- 已绑定 -->
          <template v-else-if="step === 'idle'">
            <div class="gf-account">
              <div class="gf-avatar" aria-hidden="true">{{ (status.nick || '闲')[0] }}</div>
              <div class="gf-acc-text">
                <div class="gf-acc-name">{{ status.nick || '已绑定' }}</div>
                <div class="muted gf-acc-sub">
                  上次同步：{{ status.last_sync || '还没同步过' }}
                </div>
              </div>
              <button class="btn btn-sm" :disabled="busy" @click="startRebind">重新绑定</button>
              <button class="btn btn-quiet btn-sm" :disabled="busy" @click="unbind">解绑</button>
            </div>

            <div class="gf-health">
              <div :class="{ ok: status.credential_saved, warn: !status.credential_saved }">
                <span>凭证</span>
                <b>{{ status.credential_saved ? '已保存' : '未保存' }}</b>
              </div>
              <div :class="{ ok: status.order_api_valid, warn: !status.order_api_valid }">
                <span>订单接口</span>
                <b>{{ orderApiLabel }}</b>
              </div>
              <div :class="{ ok: status.bridge_online, warn: !status.bridge_online }">
                <span>浏览器扩展</span>
                <b>{{ bridgeLabel }}</b>
                <small>{{ bridgeDetail }}</small>
              </div>
            </div>

            <details class="gf-extension-card" :open="status.bridge_auth_required && !status.bridge_token_configured">
              <summary>
                <span><b>浏览器扩展设置</b><small>{{ status.bridge_token_configured ? '配对码已配置' : (status.bridge_auth_required ? '需要配对码' : '可直接连接') }}</small></span>
                <span class="gf-extension-chevron" aria-hidden="true">›</span>
              </summary>
              <div class="gf-extension-body">
                <p>安装后不需要保持专用浏览器窗口，也不需要托盘。浏览器会后台续期登录态并在变化后自动上报；电脑关机时服务器继续拉单，下次打开浏览器会自动恢复检查。</p>
                <ol>
                  <li>下载并解压扩展包，运行 <code>install_goofish_extension.bat</code>。</li>
                  <li>在扩展页启用「开发人员模式」，选择「加载解压缩的扩展」，指向刚解压的目录。</li>
                  <li>点击浏览器工具栏里的扩展图标，在快捷面板填写服务器和店铺；如需配对码，点下方生成后填入。</li>
                  <li>保存连接后，点扩展里的「打开官方扫码登录」，用闲鱼 App 完成官方扫码。</li>
                  <li>面板显示「当前可用」且服务器、配对码、闲鱼三项正常，即表示连接可用。</li>
                </ol>
                <a class="btn btn-sm gf-extension-download" href="/downloads/goofish-extension.zip" download="goofish-extension.zip">下载最新扩展包</a>
                <div v-if="bridgeToken" class="gf-extension-token">
                  <code>{{ bridgeToken }}</code>
                  <button class="btn btn-sm" @click="copyBridgeToken">{{ bridgeTokenCopied ? '已复制' : '复制' }}</button>
                </div>
                <div class="gf-extension-actions">
                  <button class="btn btn-sm btn-primary" :disabled="bridgeTokenBusy" @click="issueBridgeToken">
                    <span v-if="bridgeTokenBusy" class="spin"></span>{{ status.bridge_token_configured ? '重新生成配对码' : '生成配对码' }}
                  </button>
                  <button v-if="status.bridge_token_configured" class="btn btn-sm btn-ghost btn-danger" :disabled="bridgeTokenBusy" @click="revokeBridgeToken">撤销</button>
                  <span class="muted">开启访问密码时必须配对；配对码只在生成时显示一次。</span>
                </div>
              </div>
            </details>

            <div class="gf-keep">
              <div class="gf-keep-text">
                <b>后台登录检查</b>
                <span class="muted">{{ status.keepalive ? '已开启' : '已关闭' }} · {{ status.sso_saved ? 'SSO 凭证已保存' : '无 SSO 凭证' }}</span>
                <HelpTip text="服务器能用 SSO 检查短期会话并刷新 _m_h5_tk 等签名令牌，但无法在闲鱼 H5 登录 cookie2 真失效后重新登录。&#10;长期稳定使用浏览器扩展：浏览器登录态变化会自动上报并校验，关机后服务器仍可用已保存凭证拉单。&#10;关闭这里只会停止服务器的后台检查，浏览器扩展仍可继续上报。" />
              </div>
              <button class="gf-switch" :class="{ on: status.keepalive }" role="switch"
                      :aria-checked="status.keepalive" @click="toggleKeepAlive">
                <i></i>
              </button>
            </div>

            <div class="gf-row">
              <div class="gf-field gf-field-inline">
                <label>自动同步</label>
                <select class="gf-select" :value="status.auto_days || 0" @change="changeAuto">
                  <option v-for="o in autoOptions" :key="o.v" :value="o.v">{{ o.t }}</option>
                </select>
              </div>
              <div class="gf-field gf-field-inline">
                <label>只同步最近</label>
                <select class="gf-select" :value="status.auto_window || 30" @change="changeWindow">
                  <option :value="3">3 天</option>
                  <option :value="7">7 天</option>
                  <option :value="15">15 天</option>
                  <option :value="30">30 天</option>
                  <option :value="60">60 天</option>
                  <option :value="90">90 天</option>
                </select>
              </div>
              <div class="gf-field gf-field-inline">
                <label>上次同步</label>
                <div class="gf-static">{{ status.last_sync || '还没同步过' }}</div>
              </div>
            </div>

            <div class="gf-row">
              <div class="gf-field gf-field-inline">
                <label>账单基准回溯（仅手动同步）<HelpTip text="闲鱼「订单管理」只覆盖近 3 个月（列表接口固定回 40 单），历史订单以收入 / 支出账单为准。这里只在点击「手动同步」时生效，不会影响「立即同步」或后台自动同步。" /></label>
                <select class="gf-select" v-model.number="backfillMonths">
                  <option :value="0">只看订单列表</option>
                  <option :value="6">近 6 个月</option>
                  <option :value="12">近 12 个月</option>
                  <option :value="24">近 24 个月</option>
                </select>
              </div>

            </div>

            <label class="gf-overwrite gf-backfill-toggle">
              <input type="checkbox" v-model="useBackfill" @change="changeBackfill" />
              <span>
                <b>手动同步时一并做账单回溯</b>
                <HelpTip text="这个开关只控制手动同步生成的订单清单。立即同步和后台自动同步始终只处理「只同步最近」里选的天数，永不回溯历史账单。此开关按店铺保存。" />
              </span>
            </label>

            <details class="gf-why gf-rules">
              <summary>同步规则说明</summary>
              <div>
                只绑定「{{ shopName }}」这一家店，其他店铺各自独立绑定、互不影响。<br />
                <b>订单列表</b>：闲鱼只给最近 3 个月（接口固定返回 40 单），所以它只用来抓「刚成交」的单；
                「已发货 / 待发货」也算成交（钱已付）会一起导入，只有退款、交易关闭、取消、待付款才跳过。<br />
                <b>立即同步</b>：按「只同步最近」选的天数直接新增订单并按订单号补正已有卖单，不显示清单，也不做账单回溯。<br />
                <b>手动同步</b>：只有点击后才生成订单清单。账单回溯只在这里生效：收入 / 支出账单按账单期逐月对账，成交价取<b>实收金额</b>、服务费取<b>实际扣款</b>
                （软件 1% + 基础 0.6%，命中无忧卖再按类目加收），到手价 = 成交价 − 服务费 − 无忧卖 − 卖运费。
                上面的「账单基准回溯」就是用来补 3 个月以前的历史订单的；清单超过 2 小时会自动返回。<br />
                同单号不会重复导入（写库前按「店铺 + 订单号」再查一次）；
                <b>自动同步会按订单号更新已有卖单的成交价、软件服务费和无忧卖服务费</b>，
                备注、商品、买家、运费、联单关联和状态保持不动。手动同步仍可逐条选择是否补正。
              </div>
            </details>

            <div v-if="pingMsg" class="tip">{{ pingMsg }}</div>
            <div v-if="err" class="tip err">{{ err }}</div>

            <div class="gf-sync-guide" role="note">
              <b>立即同步</b><span>按最近 {{ status.auto_window || 30 }} 天后台入库，速度快，不显示清单。</span>
              <b>手动同步</b><span>生成订单清单逐条筛选；只有这里能用账单回溯补历史订单。</span>
            </div>

            <div class="gf-actions">
              <button class="btn btn-quiet" :disabled="busy || pingBusy" @click="ping">
                <span v-if="pingBusy" class="spin"></span>{{ pingBusy ? '检测中…' : '检测连接' }}
              </button>
              <button class="btn" :disabled="busy" @click="pull">
                <span v-if="busy && syncMode === 'manual'" class="spin"></span>{{ busy && syncMode === 'manual' ? '拉取清单中' : '手动同步' }}
              </button>
              <button class="btn btn-primary" :disabled="busy" @click="syncNow">
                <span v-if="busy && syncMode === 'auto'" class="spin"></span>{{ busy && syncMode === 'auto' ? '同步中' : '立即同步' }}
              </button>
            </div>
          </template>

          <!-- 拉取中 -->
          <template v-else-if="step === 'loading'">
            <div class="gf-loading">
              <span class="spin big"></span>
              <div class="gf-loading-text">{{ progress.text || loadingText }}</div>
              <div class="gf-progress"><i :style="{ width: Math.max(2, progress.pct) + '%' }"></i></div>
              <div class="muted gf-acc-sub">
                {{ syncMode === 'manual' ? '订单列表 → 账单核对 → 生成清单' : `仅处理最近 ${status.auto_window || 30} 天，不回溯历史账单` }}
                · {{ Math.round(progress.pct) }}%
              </div>
            </div>
          </template>

          <!-- 预览 -->
          <template v-else-if="step === 'review'">
            <div class="gf-stat">
              <span class="gf-chip ok">可导入 {{ stat.fresh }}</span>
              <span class="gf-chip">已在库 {{ stat.duplicate }}</span>
              <span class="gf-chip">跳过 {{ stat.skipped }}</span>
              <span class="gf-chip muted-chip">已扣费 {{ stat.billed }}</span>
              <span class="gf-chip muted-chip">待结算 {{ stat.unbilled }}</span>
              <span v-if="keptManual" class="gf-chip muted-chip">手动保留 {{ keptManual }}</span>
            </div>

            <!-- 闲鱼卖单列表接口固定只回最近 40 单，更早的订单拿不到。
                 不写清楚的话，用户会以为「同步过了就齐了」。 -->
            <div class="gf-cap muted">
              订单列表只覆盖<b>近 3 个月</b>（接口固定返回 40 单，翻页与时间筛选都无效）。
              更早的订单以<b>收入 / 支出账单</b>为准——上面选了回溯月数，就会从账单里补出来。
            </div>

            <div v-if="plan && plan.warnings && plan.warnings.length" class="tip err gf-warn">
              {{ plan.warnings[0] }}
              <span v-if="plan.warnings.length > 1"> 等 {{ plan.warnings.length }} 条</span>
            </div>

            <div class="gf-toolbar">
              <div class="gf-toolbar-main">
                <label class="gf-check">
                  <input type="checkbox" v-model="allPicked" /> 全选可导入项
                </label>
                <span class="muted">共 {{ rows.length }} 条</span>
              </div>
              <span class="gf-review-clock" :class="{ urgent: reviewRemaining < 2 * 60 * 1000 }">
                清单将在 {{ reviewRemainingLabel }} 后自动返回
              </span>
            </div>

            <div class="gf-table-wrap">
              <table class="gf-table">
                <thead>
                  <tr>
                    <th class="c-pick"></th>
                    <th>商品</th>
                    <th class="c-num c-price">成交价</th>
                    <th class="c-num c-fee">服务费</th>
                    <th class="c-num c-net">到手</th>
                    <th class="c-time">时间</th>
                    <th class="c-st">状态</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="r in rows" :key="r.order_no + '|' + r.side"
                      :class="{ 'is-skip': !!r.skip_reason, 'is-dup': r.exists && !r.skip_reason }">
                    <td class="c-pick">
                      <input v-if="importable(r)" type="checkbox"
                             :checked="!!picked[r.order_no + '|' + r.side]"
                             @change="picked[r.order_no + '|' + r.side] = $event.target.checked" />
                      <input v-else-if="willOverwrite(r)" type="checkbox" class="pick-refresh"
                             :checked="!!refreshPicked[r.order_no]" :title="r.billed ? '按账单补正这一条：卖出价 / 服务费 / 无忧卖' : '按闲鱼订单列表金额覆盖这一条，出账后自动更正'"
                             @change="refreshPicked[r.order_no] = $event.target.checked" />
                    </td>
                    <td>
                      <div class="gf-prod">
                        <i class="gf-side" :class="r.side">{{ r.side === 'buy' ? '买' : '卖' }}</i>
                        <span class="gf-prod-name">{{ r.product || '（无标题）' }}</span>
                      </div>
                      <div class="gf-prod-sub mono">{{ r.order_no }}</div>
                    </td>
                    <td class="c-num c-price mono">{{ money(r.price) }}</td>
                    <td class="c-num c-fee mono">{{ r.fee || r.wuyou ? money(r.fee + r.wuyou) : '—' }}</td>
                    <td class="c-num c-net mono strong">{{ money(r.net) }}</td>
                    <td class="c-time muted">{{ dayOf(r.time) }}</td>
                    <td class="c-st">
                      <span v-if="r.backfill" class="gf-tag backfill" title="列表接口只回最近 40 单，这条是从「收入明细」账单里补出来的">账单补建</span>
                      <span v-else-if="willOverwrite(r)" class="gf-tag overwrite"
                            :title="allowOverwrite && refreshPicked[r.order_no] ? (r.billed ? '将按最新账单补正卖出价 / 服务费 / 无忧卖' : '账单尚未生成，将以闲鱼订单列表金额覆盖，出账后自动更正') : '这一条的金额原样保留'">
                        <template v-if="allowOverwrite && refreshPicked[r.order_no]">
                          <span v-if="r.billed">将补正</span>
                          <template v-else>
                            <span>将覆盖</span>
                            <small>待账单</small>
                          </template>
                        </template>
                        <template v-else>
                          <span>可补正</span>
                          <small>保留</small>
                        </template>
                      </span>
                      <span v-else-if="r.exists && !r.skip_reason" class="gf-tag dup">已在库</span>
                      <span v-else-if="r.skip_reason" class="gf-tag skip" :title="r.status">{{ r.skip_reason }}</span>
                      <span v-else-if="!r.billed" class="gf-tag wait">待结算</span>
                      <span v-else class="gf-tag new">可导入</span>
                    </td>
                  </tr>
                  <tr v-if="!rows.length">
                    <td colspan="7" class="gf-empty">没有拉到订单</td>
                  </tr>
                </tbody>
              </table>
            </div>

            <label class="gf-overwrite">
              <input type="checkbox" v-model="allowOverwrite" />
              <span>
                <b>覆盖已在库卖单的金额</b>
                <i v-if="refreshable">（{{ refreshable }} 条可补正，已选 {{ refreshCount }} 条）</i>
                <em>手动补正默认关闭；勾选后会全选确实需要补正的订单，也可以逐条取消：已出账用实收金额，未出账只更新订单列表里的成交价，已有服务费不会被清零。<b>备注、联单关联、完结、弃单状态永远不动</b>。</em>
              </span>
            </label>

            <div v-if="err" class="tip err">{{ err }}</div>

            <div class="gf-actions">
              <button class="btn btn-quiet" :disabled="busy" @click="backToIdle">返回</button>
              <button class="btn btn-primary" :disabled="busy || (!pickedCount && !(allowOverwrite && refreshCount))" @click="apply">
                <span v-if="busy" class="spin"></span>
                <template v-if="pickedCount">导入 {{ pickedCount }} 条到「{{ shopName }}」</template>
                <template v-else-if="allowOverwrite && refreshCount">按账单补正 {{ refreshCount }} 条</template>
                <template v-else>没有可导入的订单</template>
              </button>
            </div>
          </template>

          <!-- 完成 -->
          <template v-else-if="step === 'done'">
            <div class="gf-done">
              <div class="gf-done-icon" aria-hidden="true">✓</div>
              <div class="gf-done-title">{{ doneTitle }}</div>
              <div class="muted" v-if="result && result.skipped">跳过 {{ result.skipped }} 条（写入失败）</div>
              <div class="muted" v-if="result && result.duplicate">{{ result.duplicate }} 条已在库，未重复导入</div>
              <div class="muted" v-if="result && result.refreshed">另按最新闲鱼数据补正了 {{ result.refreshed }} 条卖单</div>
              <div class="muted" v-if="result && result.kept_manual">有 {{ result.kept_manual }} 条手动录入的记录，按你的设置原样保留</div>
              <div class="muted" v-if="result && result.message">{{ result.message }}</div>
              <div class="muted">导入到「{{ shopName }}」</div>
            </div>
            <div class="gf-actions">
              <button class="btn btn-quiet" @click="backToIdle">再同步一次</button>
              <button class="btn btn-primary" @click="emit('close')">完成</button>
            </div>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

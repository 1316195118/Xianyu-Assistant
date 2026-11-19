<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { shops, activeId, loadShops } from '../shopStore'
import HelpTip from './HelpTip.vue'
import { maskCloser } from '../maskClose'
import { gfStatus, gfRebindRequest } from '../gfState'
import { onRealtime } from '../realtime'
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
onUnmounted(() => { stopQR(); if (offProgress) offProgress() })

// step: bind（未绑定）/ idle（已绑定）/ loading / review / done
const step = ref('idle')
// 重新绑定：保留当前账号，只是换一份凭证，不用先解绑
const rebinding = ref(false)
const progress = ref({ on: false, pct: 0, text: '' })
let offProgress = null
const status = ref({ logged_in: false, nick: '', saved_at: '', last_sync: '', masked: '', keepalive: true, expired: false, auto_days: 0, auto_window: 30, backfill: true, syncing: false })
const cookieInput = ref('')
const showHelp = ref(false)
const pingMsg = ref('')
// 是否允许按账单补正「已在库」记录的金额。默认关：用户明确要求「默认放弃覆盖」。
// 注意：联单关联、完结、弃单状态任何时候都不会被同步改动。
const allowOverwrite = ref(false)
const plan = ref(null)
// 历史补建：列表接口只回最近 40 单，这里可以按账单期往前补
const backfillMonths = ref(12)
// 立即同步时是否一并做账单回溯。值由后端按店铺持久化（status.backfill），
// 刷新/退出再进都会保持上次的选择；默认开（沿用原行为）。
const useBackfill = ref(true)
const picked = ref({})
const err = ref('')
const busy = ref(false)
// 「检测连接」单独一个 busy：它和「立即同步」并存，共用一个会把另一个按钮也带上假的「检测中」
const pingBusy = ref(false)
const result = ref(null)

const rows = computed(() => (plan.value && plan.value.rows) || [])
const stat = computed(() => (plan.value && plan.value.stat) || {})

const importable = (r) => !r.skip_reason && !r.exists
// 已在库的卖单里，同步建的会被「按账单补正」，手动录的会原样保留
const willOverwrite = (r) => r.exists && r.billed && r.side === 'sell' && r.managed
const willKeep = (r) => r.exists && r.billed && r.side === 'sell' && !r.managed
const pickedList = computed(() => rows.value.filter((r) => importable(r) && picked.value[r.order_no + '|' + r.side]))
// 已在库、但这次拿到了真实扣费的卖单：点一次「导入」会顺手把它们的服务费与到手价补正
const refreshable = computed(() => rows.value.filter(willOverwrite).length)
const keptManual = computed(() => rows.value.filter(willKeep).length)
const pickedCount = computed(() => pickedList.value.length)
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

const shopName = computed(() => status.value.shop_name || (shops.value.find((x) => x.id === activeId.value) || {}).name || '当前店铺')

const money = (v) => {
  const n = Number(v) || 0
  return n.toFixed(2).replace(/\.00$/, '')
}
const dayOf = (t) => String(t || '').slice(0, 10) || '—'

async function loadStatus() {
  try {
    const s = await api.goofishStatus()
    status.value = s
    // 同步给顶栏常驻状态点：别的页面也能看到失效，不用先进设置页
    gfStatus.value = {
      logged_in: !!s.logged_in,
      expired: !!s.expired,
      sync_error: s.sync_error || '',
      nick: s.nick || ''
    }
    // 恢复上次保存的账单回溯开关（后端按店铺持久化）
    useBackfill.value = s.backfill !== false
    step.value = s.logged_in ? 'idle' : 'bind'
    pingMsg.value = ''
    // 自动同步失败会存 sync_error，页面一打开就能看到具体原因（不用等手动同步）
    if (s.sync_error) pingMsg.value = s.sync_error + '，请重新绑定'
    else if (s.expired) pingMsg.value = '上次检测时登录态已失效，建议重新绑定' 
  } catch (e) {
    step.value = 'bind'
    err.value = e.message || '状态读取失败'
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
    emit('notify', on ? '已开启自动续期（同步前自动用淘宝凭证静默换发会话）' : '已关闭自动续期')
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
    emit('notify', `最近 ${r.window || w} 天：自动同步、以及回溯关闭时的立即同步都按此过滤`)
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
      ? '立即同步将一并做账单回溯'
      : `立即同步只处理最近 ${status.value.auto_window || 30} 天的订单（天数在「只同步最近」里改）`)
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
    progress.value = { on: true, pct: 100, text: '完成' }
    step.value = 'review'
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

async function apply() {
  const canRefresh = allowOverwrite.value && refreshable.value > 0
  if (busy.value || (!pickedCount.value && !canRefresh)) return
  busy.value = true
  err.value = ''
  try {
    const r = await api.goofishApply({
      plan_id: plan.value.plan_id,
      order_nos: pickedList.value.map((x) => x.order_no),
      refresh: allowOverwrite.value
    })
    result.value = r
    step.value = 'done'
    emit('refresh')
    if (r.added) emit('notify', `已导入 ${r.added} 条到「${shopName.value}」`)
    if (r.refreshed) emit('notify', `已按最新账单补正 ${r.refreshed} 条的服务费与到手价`)
  } catch (e) {
    err.value = e.message || '导入失败'
  } finally {
    busy.value = false
  }
}

function backToIdle() {
  plan.value = null
  picked.value = {}
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
onMounted(async () => {
  offProgress = onRealtime((ev) => {
    if (!ev || ev.kind !== 'progress') return
    if (ev.shop_id && activeId.value && ev.shop_id !== activeId.value) return
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
              <template v-else>正在为「{{ shopName }}」绑定。绑定后每次点「立即同步」，服务端自己拉订单和账单，不用装插件、也不用导出文件。</template>
            </p>

            <div class="gf-steps">
              <div class="gf-step"><b>1</b><span>电脑浏览器打开 <code>www.goofish.com</code> 并登录</span></div>
              <div class="gf-step"><b>2</b><span>按 <kbd>F12</kbd>，切到「Network / 网络」面板</span></div>
              <div class="gf-step"><b>3</b><span>随便点一下「我买到的」，找到任意 <code>mtop</code> 请求</span></div>
              <div class="gf-step"><b>4</b><span>在「Request Headers」里找到 <code>Cookie:</code>，整行复制</span></div>
            </div>

            <div class="gf-field">
              <label>粘贴 Cookie<HelpTip text="凭证只存在你自己的服务器数据库里，用来代替你访问闲鱼接口，不会上传到任何第三方。&#10;绑定后服务器会在每次同步前自动换发会话，正常情况下不用反复粘贴；闲鱼改密码或主动退出登录后需要重新绑定。" /></label>
              <textarea v-model="cookieInput" class="gf-textarea" rows="5"
                        placeholder="直接粘贴整行，例如：cna=xxx; _m_h5_tk=xxx; cookie2=xxx; unb=xxx; …"></textarea>
            </div>


            <div class="gf-qr-sep"><span>或者</span></div>
            <button class="btn gf-qr-btn" :disabled="busy" @click="startQR">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><path d="M14 14h3v3h-3zM20 20h.01"/></svg>
              扫码登录（实验性）
            </button>
            <div class="gf-qr-tip muted">闲鱼设备指纹校验严格，扫码可能提示「二维码失效」；若失败，请用电脑上双击 <code>goofish_bind.bat</code> 一键绑定（可靠）。绑定后靠自动续期，一般几个月才需重绑一次。</div>

            <div v-if="qrOpen" class="gf-qr-panel">
              <div class="gf-qr-head">
                <b>用闲鱼 App 扫码登录</b>
                <button class="x" @click="stopQR" aria-label="关闭扫码">×</button>
              </div>
              <img v-if="qrImg" class="gf-qr-img" :src="qrImg" alt="闲鱼登录二维码" />
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

            <div class="gf-keep">
              <div class="gf-keep-text">
                <b>自动续期</b>
                <span class="muted">{{ status.keepalive ? '已开启' : '已关闭' }}</span>
                <HelpTip text="开启后，每次同步前服务器会自动用保存的淘宝 SSO 凭证静默换发一次闲鱼会话（复刻网页版「一键快速登录」），会话过期也能自愈，不再需要定期心跳。&#10;关闭后不做换发，会话过期需重新绑定。" />
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
                <label>账单基准回溯<HelpTip text="闲鱼「订单管理」只覆盖近 3 个月（列表接口固定回 40 单），历史订单以收入 / 支出账单为准：按账单期往前翻，把这段时间已成交、系统里还没有的订单补进预览。&#10;取消下方的勾选则不翻账单，只取「只同步最近」选的天数。" /></label>
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
                <b>立即同步时一并做账单回溯</b>
                <HelpTip text="默认开启。取消勾选后，立即同步只处理「只同步最近」里选的天数（不翻历史账单），上面选的回溯月数会保留。此开关按店铺保存，退出再进仍保持上次选择。" />
              </span>
            </label>

            <details class="gf-why gf-rules">
              <summary>同步规则说明</summary>
              <div>
                只绑定「{{ shopName }}」这一家店，其他店铺各自独立绑定、互不影响。<br />
                <b>订单列表</b>：闲鱼只给最近 3 个月（接口固定返回 40 单），所以它只用来抓「刚成交」的单；
                「已发货 / 待发货」也算成交（钱已付）会一起导入，只有退款、交易关闭、取消、待付款才跳过。<br />
                <b>账单</b>：收入 / 支出账单按账单期逐月对账，成交价取<b>实收金额</b>、服务费取<b>实际扣款</b>
                （软件 1% + 基础 0.6%，命中无忧卖再按类目加收），到手价 = 成交价 − 服务费 − 无忧卖 − 卖运费。
                上面的「账单基准回溯」就是用来补 3 个月以前的历史订单的。<br />
                同单号不会重复导入（写库前按「店铺 + 订单号」再查一次）；
                <b>手动录入的记录不会被覆盖</b>，只有「闲鱼同步」建的那批才会按账单补正。
              </div>
            </details>

            <div v-if="pingMsg" class="tip">{{ pingMsg }}</div>
            <div v-if="err" class="tip err">{{ err }}</div>

            <div class="gf-actions">
              <button class="btn btn-quiet" :disabled="busy || pingBusy" @click="ping">
                <span v-if="pingBusy" class="spin"></span>{{ pingBusy ? '检测中…' : '检测连接' }}
              </button>
              <button class="btn btn-primary" :disabled="busy" @click="pull">
                <span v-if="busy" class="spin"></span>{{ busy ? '拉取中' : '立即同步' }}
              </button>
            </div>
          </template>

          <!-- 拉取中 -->
          <template v-else-if="step === 'loading'">
            <div class="gf-loading">
              <span class="spin big"></span>
              <div class="gf-loading-text">{{ progress.text || '正在拉取订单与账单…' }}</div>
              <div class="gf-progress"><i :style="{ width: Math.max(2, progress.pct) + '%' }"></i></div>
              <div class="muted gf-acc-sub">订单列表 → 账单核对 → 生成预览，通常 10 秒左右（{{ Math.round(progress.pct) }}%）</div>
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
              <label class="gf-check">
                <input type="checkbox" v-model="allPicked" /> 全选可导入项
              </label>
              <span class="muted">共 {{ rows.length }} 条</span>
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
                      <input type="checkbox" :disabled="!importable(r)"
                             :checked="!!picked[r.order_no + '|' + r.side] && importable(r)"
                             @change="picked[r.order_no + '|' + r.side] = $event.target.checked" />
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
                      <span v-else-if="willKeep(r)" class="gf-tag manual" title="这条是你手动录入的，同步不会改动它的金额">手动·保留</span>
                      <span v-else-if="willOverwrite(r)" class="gf-tag overwrite"
                            :title="allowOverwrite ? '勾选了覆盖：将按最新账单补正服务费与到手价' : '未勾选覆盖：这一条的金额原样保留'">
                        {{ allowOverwrite ? '将补正' : '可补正·保留' }}
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
                <b>按账单覆盖已在库记录的金额</b>
                <i v-if="refreshable">（{{ refreshable }} 条可补正）</i>
                <em>默认不勾选＝只新增、不动任何老数据。勾选后仅更新服务费 / 成交价 / 到手价；<b>联单关联、完结、弃单状态永远不动</b>。</em>
              </span>
            </label>

            <div v-if="err" class="tip err">{{ err }}</div>

            <div class="gf-actions">
              <button class="btn btn-quiet" :disabled="busy" @click="backToIdle">返回</button>
              <button class="btn btn-primary" :disabled="busy || (!pickedCount && !(allowOverwrite && refreshable))" @click="apply">
                <span v-if="busy" class="spin"></span>
                <template v-if="pickedCount">导入 {{ pickedCount }} 条到「{{ shopName }}」</template>
                <template v-else-if="allowOverwrite && refreshable">按账单补正 {{ refreshable }} 条</template>
                <template v-else>没有可导入的订单</template>
              </button>
            </div>
          </template>

          <!-- 完成 -->
          <template v-else-if="step === 'done'">
            <div class="gf-done">
              <div class="gf-done-icon" aria-hidden="true">✓</div>
              <div class="gf-done-title">已导入 {{ result && result.added }} 条</div>
              <div class="muted" v-if="result && result.skipped">跳过 {{ result.skipped }} 条（写入失败）</div>
              <div class="muted" v-if="result && result.refreshed">另按最新账单补正了 {{ result.refreshed }} 条的服务费与到手价</div>
              <div class="muted" v-if="result && result.kept_manual">有 {{ result.kept_manual }} 条手动录入的记录，按你的设置原样保留</div>
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

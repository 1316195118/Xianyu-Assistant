<script setup>
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { api } from '../api'
import { confirm } from '../confirm'
import { maskCloser } from '../maskClose'
import {
  dailyUpdateCheckEnabled,
  dailyUpdateCheckedToday,
  dailyUpdateCheckHint,
  setDailyUpdateCheckEnabled,
  markDailyUpdateChecked,
  markUpdateReleaseNotified
} from '../updatePreferences'
// 遮罩点击守卫：在输入框里拖选文字划出弹窗松手时，不会误关弹窗
const mask = maskCloser(() => close)


const emit = defineEmits(['close', 'notify'])
const data = ref(null)
const loading = ref(true)
const err = ref('')
const expanded = ref('')
const panelEl = ref(null)
const bodyEl = ref(null)
const closeBtn = ref(null)
let lastFocus = null

function close() { emit('close') }
function notify(text, err = false, opts = {}) {
  emit('notify', text, err, null, opts)
}
function onKey(e) {
  // 确认框层级高于版本窗口；Esc 应先交给确认框取消，而不是把下层窗口关掉。
  if (e.key === 'Escape') {
    if (document.querySelector('.confirm-mask')) return
    e.preventDefault(); e.stopPropagation(); close()
  }
}
onMounted(async () => {
  lastFocus = document.activeElement
  document.body.classList.add('overlay-open')
  window.addEventListener('keydown', onKey, true)
  await nextTick()
  closeBtn.value?.focus?.()
  try { data.value = await api.version() }
  catch (e) { err.value = e.message || '版本信息加载失败' }
  finally { loading.value = false }
})
onUnmounted(() => {
  stopCheckProgress()
  stopProgressPolling()
  window.removeEventListener('keydown', onKey, true)
  document.body.classList.remove('overlay-open')
  try { lastFocus?.focus?.() } catch (e) { /* ignore */ }
})

// 移动端底部 sheet：与录单抽屉 / 确认框 / 回收站同一套下拉关闭手感
let dragY = null
let dragTop = 0
function onHeadDown(e) {
  if (window.innerWidth > 600) return
  const el = panelEl.value
  if (!el) return
  if (bodyEl.value && bodyEl.value.scrollTop > 0) return
  const y = e.touches[0].clientY
  if (y - el.getBoundingClientRect().top > 96) return
  dragY = y
  dragTop = 0
}
function onHeadMove(e) {
  if (dragY == null) return
  const d = e.touches[0].clientY - dragY
  if (d <= 0) return
  e.preventDefault()
  dragTop = Math.min(160, d * 0.55)
  if (panelEl.value) panelEl.value.style.transform = `translateY(${dragTop}px)`
}
function onHeadEnd() {
  if (dragY == null) return
  const shouldClose = dragTop > 64
  dragY = null
  const el = panelEl.value
  if (!el) return
  if (shouldClose) {
    el.style.transition = ''
    el.style.transform = ''
    close()
    return
  }
  el.style.transition = 'transform var(--dur-2) var(--ease)'
  el.style.transform = ''
  setTimeout(() => { if (panelEl.value) panelEl.value.style.transition = '' }, 260)
}

// 更新说明是轻量 Markdown：## 小标题 / - 列表 / 普通段落
function noteLines(note) {
  return String(note || '').split('\n').map(raw => {
    const s = raw.trim()
    if (!s) return { t: 'gap', x: '' }
    if (s.startsWith('## ')) return { t: 'h', x: s.slice(3) }
    if (s.startsWith('- ')) return { t: 'li', x: s.slice(2) }
    return { t: 'p', x: s }
  })
}
const firstLine = (s) => {
  const l = String(s || '').split('\n').map(x => x.trim()).find(x => x && !x.startsWith('##') && !x.startsWith('<!--'))
  return (l || '常规构建').replace(/^[-#]+\s*/, '').slice(0, 60)
}
const toggle = (v) => { expanded.value = expanded.value === v ? '' : v }

// ---------- 在线更新 ----------
const upd = ref(null)
const updBusy = ref(false)
const updErr = ref('')
const confirming = ref(false)
const applying = ref(false)
const applyText = ref('')
const liveProgress = ref(null)
const updNotesOpen = ref(false)
const cfgOpen = ref(false)
const cfgBusy = ref(false)
const cfgMsg = ref('')
const cfg = ref({ proxy: '' })
const dailyCheck = ref(dailyUpdateCheckEnabled())
const dailyHint = ref(dailyUpdateCheckHint())
let checkTimer = null
let progressTimer = null
let checkSeq = 0
let progressPollBusy = false
const progressPercent = computed(() => {
  const n = Number(liveProgress.value?.percent)
  return Number.isFinite(n) ? Math.min(100, Math.max(0, Math.round(n))) : 0
})
const progressMessage = computed(() =>
  liveProgress.value?.message || (applying.value ? applyText.value : (updBusy.value ? '正在检查更新…' : '')))
const progressVisible = computed(() => !!progressMessage.value && (updBusy.value || confirming.value || applying.value || !!liveProgress.value))
const progressStageLabel = computed(() => {
  const stage = liveProgress.value?.stage
  if (stage === 'confirming') return '等待确认'
  if (stage === 'checked') return '检查完成'
  if (stage === 'error') return '操作失败'
  if (stage === 'done') return '更新完成'
  if (stage === 'downloading') return '下载安装包'
  if (stage === 'verifying') return '安全校验'
  if (stage === 'installing') return '安装新版本'
  if (stage === 'restarting') return '等待服务恢复'
  return applying.value ? '准备更新' : '检查最新版本'
})
const progressDetail = computed(() => {
  const p = liveProgress.value || {}
  if (p.stage === 'confirming') return '确认后会立即下载、校验并安装新版本'
  if (p.stage === 'downloading' && Number(p.total_bytes) > 0) {
    return `已下载 ${fmtBytes(p.downloaded_bytes)} / ${fmtBytes(p.total_bytes)}`
  }
  if (p.stage === 'verifying') return '文件下载完成，正在核对发布时的 SHA-256'
  if (p.stage === 'installing') return '校验通过，正在替换程序，请勿关闭容器'
  if (p.stage === 'restarting') return '服务恢复后会自动确认版本并刷新页面'
  if (p.stage === 'done') return '新版本已经运行，页面即将自动刷新'
  if (p.stage === 'error') return '可查看下方原因，修正后重新操作'
  if (p.stage === 'checked') return '检查结果已保留，可继续查看版本状态'
  return '正在从项目 Release 读取最新版本信息'
})
const progressSteps = computed(() => {
  const stage = liveProgress.value?.stage
  if (stage === 'confirming') {
    return [{ key: 'confirming', label: '确认更新' }]
  }
  if (stage === 'checking' || stage === 'checked' || (stage === 'error' && !applying.value)) {
    return [{ key: 'checking', label: '检查版本' }]
  }
  return [
    { key: 'checking', label: '检查版本' },
    { key: 'downloading', label: '下载安装包' },
    { key: 'verifying', label: '安全校验' },
    { key: 'restarting', label: '安装并重启' }
  ]
})
const progressStepIndex = computed(() => {
  const stage = liveProgress.value?.stage
  if (stage === 'downloading') return 1
  if (stage === 'verifying') return 2
  if (stage === 'installing' || stage === 'restarting' || stage === 'done') return 3
  if (stage === 'error') {
    if (!applying.value) return 0
    const p = progressPercent.value
    return p >= 92 ? 3 : p >= 84 ? 2 : p >= 8 ? 1 : 0
  }
  return 0
})
const progressDone = computed(() => ['checked', 'done'].includes(liveProgress.value?.stage))
const progressBad = computed(() => liveProgress.value?.stage === 'error')
function fmtBytes(n) {
  n = Number(n) || 0
  if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB'
  if (n >= 1024) return (n / 1024).toFixed(1) + ' KB'
  return Math.round(n) + ' B'
}
const updSize = computed(() => {
  const n = Number(upd.value?.asset?.size || 0)
  return n ? fmtBytes(n) : ''
})

function stopCheckProgress() {
  if (checkTimer) {
    clearInterval(checkTimer)
    checkTimer = null
  }
}

function startCheckProgress() {
  stopCheckProgress()
  const seq = ++checkSeq
  let pct = 5
  liveProgress.value = {
    active: true, stage: 'checking', percent: pct,
    message: '正在检查项目最新版本…'
  }
  checkTimer = setInterval(() => {
    if (seq !== checkSeq || !updBusy.value || applying.value) return
    pct = Math.min(32, pct + 2)
    liveProgress.value = { ...(liveProgress.value || {}), percent: pct }
  }, 480)
}

function finishCheckProgress(ok, message) {
  stopCheckProgress()
  liveProgress.value = {
    ...(liveProgress.value || {}), active: false, percent: 100,
    stage: ok ? 'checked' : 'error', message, error: ok ? '' : message
  }
  const seq = checkSeq
  setTimeout(() => {
    if (seq === checkSeq && !applying.value) liveProgress.value = null
  }, 4200)
}

function stopProgressPolling() {
  if (progressTimer) {
    clearInterval(progressTimer)
    progressTimer = null
  }
}

async function pollUpdateProgress() {
  if (progressPollBusy) return
  progressPollBusy = true
  try {
    const p = await api.updateProgress()
    if (p && (p.active || p.stage)) liveProgress.value = p
  } catch (e) { /* 服务重启期间短暂断开是正常的 */ }
  finally { progressPollBusy = false }
}

function startProgressPolling() {
  stopProgressPolling()
  pollUpdateProgress()
  progressTimer = setInterval(pollUpdateProgress, 650)
}

async function checkUpdate() {
  if (updBusy.value || confirming.value || applying.value) return
  updBusy.value = true; updErr.value = ''
  markDailyUpdateChecked()
  dailyHint.value = dailyUpdateCheckHint()
  startCheckProgress()
  notify('正在检查 GitHub Release 最新版本…', false, { tone: 'info', ttl: 5000 })
  try {
    const r = await api.updateCheck()
    upd.value = r
    updNotesOpen.value = !!(r.has_update && String(r.notes || '').trim())
    cfg.value.proxy = r.proxy || ''
    if (r.has_update) markUpdateReleaseNotified(r)
    if (r.error) {
      finishCheckProgress(false, '检查完成，但网络或发布信息需要处理')
      notify('检查更新失败：' + r.error, true, { ttl: 7000 })
    } else if (r.has_update) {
      finishCheckProgress(true, `检查完成：发现新版本 v${r.latest}`)
      notify(`发现新版本 v${r.latest}，现在可以更新`, false, { tone: 'success', ttl: 6000 })
    } else {
      finishCheckProgress(true, '检查完成：当前已经是最新版本')
      notify('检查完成：当前已经是最新版本', false, { tone: 'info' })
    }
  } catch (e) {
    updErr.value = e.message || '检查更新失败'
    finishCheckProgress(false, '检查更新失败：' + updErr.value)
    notify('检查更新失败：' + updErr.value, true, { ttl: 7000 })
  } finally { updBusy.value = false }
}

function changeDailyCheck() {
  setDailyUpdateCheckEnabled(dailyCheck.value)
  dailyHint.value = dailyUpdateCheckHint()
  if (dailyCheck.value && !dailyUpdateCheckedToday()) checkUpdate()
}

async function saveCfg() {
  if (cfgBusy.value) return
  cfgBusy.value = true; cfgMsg.value = ''
  try {
    await api.saveUpdateConfig({ proxy: cfg.value.proxy.trim() })
    cfgMsg.value = '已保存'
    await checkUpdate()
  } catch (e) {
    cfgMsg.value = e.message || '保存失败'
  } finally { cfgBusy.value = false }
}

// 重启期间接口会断，轮询到「报的版本号变成新版」才算真的更新成功。
// 只认版本号，不认「进程还在」——旧进程也可能一直活着。
async function waitNewVersion(expect) {
  const end = Date.now() + 90000
  while (Date.now() < end) {
    await new Promise((r) => setTimeout(r, 1500))
    const left = Math.max(0, Math.ceil((end - Date.now()) / 1000))
    liveProgress.value = {
      ...(liveProgress.value || {}), active: true, stage: 'restarting',
      message: `新版本正在启动，等待服务恢复…（最多 ${left} 秒）`,
      percent: Math.max(96, progressPercent.value)
    }
    try {
      const res = await fetch('/api/health', { cache: 'no-store' })
      if (!res.ok) continue
      const j = await res.json()
      if (String(j.version || '') === String(expect)) return true
    } catch (e) { /* 重启中连不上是正常的 */ }
  }
  return false
}

async function applyUpdate() {
  if (!upd.value?.latest || confirming.value || applying.value) return
  confirming.value = true
  applyText.value = `等待确认更新到 v${upd.value.latest}…`
  updErr.value = ''
  liveProgress.value = {
    active: true, stage: 'confirming', percent: 4,
    message: `已收到更新请求，请确认是否更新到 v${upd.value.latest}`,
    from: upd.value.current, to: upd.value.latest
  }
  notify(`已准备好更新到 v${upd.value.latest}，请在确认框中点击「开始更新」`, false, { tone: 'info', ttl: 6000 })
  let ok = false
  try {
    ok = await confirm({
      title: `更新到 v${upd.value.latest}？`,
      message: '程序会自行下载新版本、替换当前程序并重启服务。',
      detail: [
        '重启期间页面会短暂断开，通常几秒。',
        '更新前会保留上一份程序（xianyu-stat.bak），出问题可手动换回。',
        '更新过程中请不要关闭容器。'
      ],
      confirmText: '开始更新'
    })
  } catch (e) {
    confirming.value = false
    liveProgress.value = null
    notify('无法打开更新确认框：' + (e.message || '未知错误'), true, { ttl: 8000 })
    return
  }
  confirming.value = false
  if (!ok) {
    applyText.value = ''
    liveProgress.value = null
    notify('已取消更新', false, { tone: 'info', ttl: 3000 })
    return
  }
  applying.value = true
  applyText.value = '正在下载并校验…'
  updErr.value = ''
  notify(`已开始更新到 v${upd.value.latest}，请保持页面和容器运行`, false, { tone: 'info', ttl: 6000 })
  liveProgress.value = {
    active: true, stage: 'starting', percent: 2,
    message: `已确认，正在准备更新到 v${upd.value.latest}…`,
    from: upd.value.current, to: upd.value.latest
  }
  startProgressPolling()
  try {
    await api.updateApply()
    applyText.value = '已下载完成，正在重启服务…'
    liveProgress.value = {
      ...(liveProgress.value || {}), active: true, stage: 'restarting',
      percent: Math.max(96, progressPercent.value),
      message: '安装包已就绪，正在重启服务…'
    }
    const okNew = await waitNewVersion(upd.value.latest)
    if (!okNew) {
      applyText.value = '没等到新版本启动。请刷新页面确认；若仍是旧版本，可到容器里执行 systemctl status xianyu-stat 查看原因。'
      liveProgress.value = {
        ...(liveProgress.value || {}), active: false, stage: 'error', percent: 100,
        message: applyText.value, error: applyText.value
      }
      stopProgressPolling()
      applying.value = false
      notify('更新未能在限定时间内完成，请刷新页面并检查服务状态', true, { ttl: 9000 })
      return
    }
    stopProgressPolling()
    liveProgress.value = {
      ...(liveProgress.value || {}), active: false, stage: 'done', percent: 100,
      message: '更新完成，正在刷新页面…'
    }
    applyText.value = '更新完成，正在刷新页面…'
    notify(`v${upd.value.latest} 更新完成，页面即将自动刷新`, false, { tone: 'success', ttl: 3000 })
    setTimeout(() => { try { location.reload() } catch (e) { /* ignore */ } }, 1800)
  } catch (e) {
    stopProgressPolling()
    updErr.value = e.message || '更新失败'
    applying.value = false
    applyText.value = ''
    liveProgress.value = {
      ...(liveProgress.value || {}), active: false, stage: 'error', percent: 100,
      message: '更新失败：' + updErr.value, error: updErr.value
    }
    notify('更新失败：' + updErr.value, true, { ttl: 9000 })
  }
}
</script>

<template>
  <Teleport to="body">
  <div class="modal-mask" @pointerdown="mask.onPointerDown" @click="mask.onClick">
    <div ref="panelEl" class="modal version-modal" role="dialog" aria-modal="true" aria-labelledby="xy-version-title"
         @touchstart.passive="onHeadDown" @touchmove="onHeadMove" @touchend.passive="onHeadEnd">
      <div class="modal-head">
        <h3 id="xy-version-title">版本信息</h3>
        <button ref="closeBtn" class="x" @click="close" aria-label="关闭版本信息">×</button>
      </div>

      <div ref="bodyEl" class="modal-body">
        <div v-if="loading" class="skel-list">
          <div v-for="i in 3" :key="i" class="skel-row"><span class="skeleton skel-line w70"></span></div>
        </div>
        <div v-else-if="err" class="tip err">{{ err }}</div>
        <template v-else-if="data">
          <div class="ver-hero">
            <div class="ver-num num">{{ data.version }}</div>
            <div class="ver-meta">第 {{ data.count }} 次编译 · {{ data.time || '—' }}</div>
            <div class="ver-build mono muted">前端构建 {{ data.build || '—' }}</div>
          </div>

          <!-- 在线更新：从 GitHub Release 拉新版本，容器内自行替换 -->
          <div class="upd-card">
            <div class="upd-head">
              <b>在线更新</b>
              <span class="muted">当前 {{ data.version }}</span>
              <button class="btn btn-sm upd-action" :class="{ 'is-busy': updBusy }"
                      :disabled="updBusy || confirming || applying" :aria-busy="updBusy" @click="checkUpdate">
                <span v-if="updBusy" class="spin"></span>
                <svg v-else width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 11a8.1 8.1 0 0 0-15.5-2M4 4v5h5"/><path d="M4 13a8.1 8.1 0 0 0 15.5 2M20 20v-5h-5"/></svg>
                <span>{{ updBusy ? '正在检查…' : '检查更新' }}</span>
              </button>
            </div>

            <div v-if="progressVisible" class="upd-progress"
                 :class="{ done: progressDone, bad: progressBad, checking: !applying && !progressDone && !progressBad }"
                 role="region" aria-label="在线更新状态">
              <div class="upd-progress-top">
                <span class="upd-progress-mark" aria-hidden="true">
                  <svg v-if="progressBad" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><path d="M12 8v5"/><path d="M12 17h.01"/><circle cx="12" cy="12" r="9"/></svg>
                  <svg v-else-if="progressDone" width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><path d="m5 12 4.4 4.4L19 7"/></svg>
                  <span v-else class="upd-progress-orbit"></span>
                </span>
                <div class="upd-progress-copy">
                  <div class="upd-progress-kicker">{{ progressStageLabel }}</div>
                  <div class="upd-progress-title" role="status" aria-live="polite">{{ progressMessage }}</div>
                  <div class="upd-progress-detail">{{ progressDetail }}</div>
                </div>
                <div class="upd-progress-percent" aria-hidden="true">
                  <b class="num">{{ progressPercent }}</b><small>%</small>
                </div>
              </div>
              <div class="upd-progress-track" role="progressbar" aria-label="更新进度"
                   aria-valuemin="0" aria-valuemax="100" :aria-valuenow="progressPercent"
                   :aria-valuetext="progressMessage + '，' + progressPercent + '%'">
                <i :style="{ width: progressPercent + '%' }"></i>
              </div>
              <div class="upd-stages" :class="{ single: progressSteps.length === 1 }">
                <div v-for="(step, i) in progressSteps" :key="step.key" class="upd-stage"
                     :class="{ active: i === progressStepIndex, complete: i < progressStepIndex || progressDone, current: i === progressStepIndex && !progressDone && !progressBad, bad: i === progressStepIndex && progressBad }">
                  <span class="upd-stage-line" aria-hidden="true"></span>
                  <span class="upd-stage-label">{{ step.label }}</span>
                </div>
              </div>
            </div>

            <label class="upd-daily">
              <input type="checkbox" v-model="dailyCheck" @change="changeDailyCheck" />
              <span class="upd-daily-copy">
                <b>每日自动检查更新</b>
                <small>每天最多检查一次；同一版本只提示一次</small>
              </span>
              <em>{{ dailyHint }}</em>
            </label>

            <div v-if="updErr" class="tip err" role="alert">{{ updErr }}</div>
            <template v-else-if="upd">
              <div v-if="upd.error" class="upd-state bad">
                <span>{{ upd.error }}</span>
                <button class="btn btn-sm" @click="cfgOpen = true">打开更新设置</button>
              </div>
              <div v-else-if="upd.has_update" class="upd-state">
                <div class="upd-new">
                  有新版本 <b>v{{ upd.latest }}</b>
                  <span class="muted" v-if="upd.published_at">· {{ upd.published_at }}</span>
                </div>
                <div class="upd-notes" v-if="String(upd.notes || '').trim()">
                  <button class="upd-notes-toggle" type="button" :aria-expanded="updNotesOpen" @click="updNotesOpen = !updNotesOpen">
                    <span class="upd-notes-copy">
                      <b>本次更新说明</b>
                      <small>{{ firstLine(upd.notes) }}</small>
                    </span>
                    <span class="upd-notes-action">{{ updNotesOpen ? '收起' : '展开查看' }}</span>
                    <svg class="upd-notes-chev" :class="{ open: updNotesOpen }" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 9l6 6 6-6"/></svg>
                  </button>
                  <div v-if="updNotesOpen" class="upd-notes-full">
                    <template v-for="(l, i) in noteLines(upd.notes)" :key="i">
                      <div v-if="l.t === 'gap'" class="upd-notes-gap"></div>
                      <h4 v-else-if="l.t === 'h'">{{ l.x }}</h4>
                      <div v-else-if="l.t === 'li'" class="upd-notes-li"><i></i><span>{{ l.x }}</span></div>
                      <p v-else>{{ l.x }}</p>
                    </template>
                  </div>
                </div>
                <div class="upd-notes missing" v-else>该版本暂未提供发布说明，可继续更新并到版本信息中查看历史记录。</div>
                <div class="upd-ops">
                  <button class="btn btn-sm btn-primary upd-action" :class="{ 'is-busy': applying }"
                          :disabled="!upd.can_apply || confirming || applying" :aria-busy="confirming || applying" @click="applyUpdate">
                    <span v-if="confirming || applying" class="spin"></span>
                    {{ confirming ? '等待确认…' : (applying ? '正在更新…' : (upd.can_apply ? '立即更新' : '需手动更新')) }}
                  </button>
                  <span v-if="!upd.can_apply" class="muted">当前是 {{ upd.platform }}，一键更新只支持 Linux (amd64) 容器</span>
                  <span v-else-if="updSize" class="muted">安装包 {{ updSize }} · 下载后先校验再替换</span>
                </div>
              </div>
              <div v-else class="upd-state ok">已是最新版本</div>
            </template>
            <div v-else class="upd-state muted">点「检查更新」看看有没有新版</div>

            <details class="upd-cfg" :open="cfgOpen" @toggle="cfgOpen = $event.target.open">
              <summary>网络代理（可选）</summary>
              <div class="upd-cfg-body">
                <label>代理（容器连不上 GitHub 时填）</label>
                <input class="input" v-model="cfg.proxy" placeholder="如 http://10.10.10.2:7890，留空则不用代理" />
                <div class="upd-cfg-ops">
                  <button class="btn btn-sm" :disabled="cfgBusy" @click="saveCfg">保存</button>
                  <span class="muted">{{ cfgMsg }}</span>
                </div>
                <div class="muted upd-cfg-hint">在线更新固定从项目公开 Release 获取，不需要仓库地址或 Token。若服务器无法直连 GitHub，可填写 HTTP 或 SOCKS5 代理；留空即关闭。</div>
              </div>
            </details>
          </div>

          <div class="ver-section-title">本次更新说明</div>
          <div class="ver-note">
            <template v-for="(l, i) in noteLines(data.note)" :key="i">
              <div v-if="l.t === 'gap'" class="ver-gap"></div>
              <h4 v-else-if="l.t === 'h'">{{ l.x }}</h4>
              <div v-else-if="l.t === 'li'" class="ver-li"><i></i><span>{{ l.x }}</span></div>
              <p v-else>{{ l.x }}</p>
            </template>
          </div>

          <div class="ver-section-title">历史更新 <span class="muted">{{ data.history?.length || 0 }} 次编译</span></div>
          <div v-if="!data.history?.length" class="muted ver-empty">暂无历史记录</div>
          <div v-else class="ver-history">
            <div v-for="h in data.history" :key="h.version" class="ver-hist-item"
                 :class="{ open: expanded === h.version }" role="button" tabindex="0"
                 @click="toggle(h.version)" @keyup.enter="toggle(h.version)">
              <span class="ver-hist-v mono">{{ h.version }}</span>
              <div class="ver-hist-main">
                <b>{{ firstLine(h.note) }}</b>
                <small class="muted">{{ h.time }} · {{ h.build || '—' }}</small>
                <div v-if="expanded === h.version" class="ver-hist-note">{{ h.note }}</div>
              </div>
              <svg class="ver-chev" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>
            </div>
          </div>
        </template>
      </div>

      <div class="modal-foot">
        <span class="muted foot-hint">说明来自 version/NEXT.md · 每次编译自动归档</span>
        <button class="btn btn-quiet" @click="close">关闭</button>
      </div>
    </div>
  </div>
  </Teleport>
</template>

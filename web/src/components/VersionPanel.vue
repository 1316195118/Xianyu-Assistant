<script setup>
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { api } from '../api'
import { confirm } from '../confirm'
import { maskCloser } from '../maskClose'
// 遮罩点击守卫：在输入框里拖选文字划出弹窗松手时，不会误关弹窗
const mask = maskCloser(() => close)


const emit = defineEmits(['close'])
const data = ref(null)
const loading = ref(true)
const err = ref('')
const expanded = ref('')
const panelEl = ref(null)
const bodyEl = ref(null)
const closeBtn = ref(null)
let lastFocus = null

function close() { emit('close') }
function onKey(e) {
  if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close() }
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
const applying = ref(false)
const applyText = ref('')
const liveProgress = ref(null)
const cfgOpen = ref(false)
const cfgBusy = ref(false)
const cfgMsg = ref('')
const cfg = ref({ repo: '', token: '', proxy: '' })
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
const progressVisible = computed(() => !!progressMessage.value && (updBusy.value || applying.value || !!liveProgress.value))
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
    message: '正在连接 GitHub 获取最新版本…'
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
  }, 1400)
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
  if (updBusy.value || applying.value) return
  updBusy.value = true; updErr.value = ''
  startCheckProgress()
  try {
    const r = await api.updateCheck()
    upd.value = r
    cfg.value.repo = r.repo || ''
    cfg.value.proxy = r.proxy || ''
    if (r.error) finishCheckProgress(false, '检查完成，但更新源需要处理')
    else if (r.has_update) finishCheckProgress(true, `检查完成：发现新版本 v${r.latest}`)
    else finishCheckProgress(true, '检查完成：当前已经是最新版本')
  } catch (e) {
    updErr.value = e.message || '检查更新失败'
    finishCheckProgress(false, '检查更新失败：' + updErr.value)
  } finally { updBusy.value = false }
}

async function saveCfg() {
  if (cfgBusy.value) return
  cfgBusy.value = true; cfgMsg.value = ''
  try {
    const r = await api.saveUpdateConfig({ repo: cfg.value.repo.trim(), token: cfg.value.token.trim(), proxy: cfg.value.proxy.trim() })
    cfg.value.token = ''
    cfgMsg.value = '已保存'
    upd.value = { ...(upd.value || {}), repo: r.repo, has_token: r.has_token }
    await checkUpdate()
  } catch (e) {
    cfgMsg.value = e.message || '保存失败'
  } finally { cfgBusy.value = false }
}

async function clearToken() {
  cfgBusy.value = true; cfgMsg.value = ''
  try {
    const r = await api.saveUpdateConfig({ repo: cfg.value.repo.trim(), proxy: cfg.value.proxy.trim(), clear_token: true })
    cfgMsg.value = 'Token 已清除'
    upd.value = { ...(upd.value || {}), repo: r.repo, has_token: r.has_token }
  } catch (e) {
    cfgMsg.value = e.message || '清除失败'
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
  if (!upd.value?.latest || applying.value) return
  const ok = await confirm({
    title: `更新到 v${upd.value.latest}？`,
    message: '程序会自行下载新版本、替换当前程序并重启服务。',
    detail: [
      '重启期间页面会短暂断开，通常几秒。',
      '更新前会保留上一份程序（xianyu-stat.bak），出问题可手动换回。',
      '更新过程中请不要关闭容器。'
    ],
    confirmText: '开始更新'
  })
  if (!ok) return
  applying.value = true
  applyText.value = '正在下载并校验…'
  updErr.value = ''
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
      return
    }
    stopProgressPolling()
    liveProgress.value = {
      ...(liveProgress.value || {}), active: false, stage: 'done', percent: 100,
      message: '更新完成，正在刷新页面…'
    }
    applyText.value = '更新完成，正在刷新页面…'
    setTimeout(() => { try { location.reload() } catch (e) { /* ignore */ } }, 700)
  } catch (e) {
    stopProgressPolling()
    updErr.value = e.message || '更新失败'
    applying.value = false
    applyText.value = ''
    liveProgress.value = {
      ...(liveProgress.value || {}), active: false, stage: 'error', percent: 100,
      message: '更新失败：' + updErr.value, error: updErr.value
    }
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
              <button class="btn btn-sm" :disabled="updBusy || applying" @click="checkUpdate">
                <span v-if="updBusy" class="spin"></span>{{ updBusy ? '检查中…' : '检查更新' }}
              </button>
            </div>

            <div v-if="updErr" class="tip err" role="alert">{{ updErr }}</div>
            <template v-else-if="upd">
              <div v-if="upd.error" class="upd-state bad">
                <span>{{ upd.error }}</span>
                <button class="btn btn-sm" @click="cfgOpen = true">填写更新源</button>
              </div>
              <div v-else-if="upd.has_update" class="upd-state">
                <div class="upd-new">
                  有新版本 <b>v{{ upd.latest }}</b>
                  <span class="muted" v-if="upd.published_at">· {{ upd.published_at }}</span>
                </div>
                <div class="upd-notes" v-if="upd.notes">{{ firstLine(upd.notes) }}</div>
                <div class="upd-ops">
                  <button class="btn btn-sm btn-primary" :disabled="!upd.can_apply || applying" @click="applyUpdate">
                    {{ applying ? '更新中…' : (upd.can_apply ? '立即更新' : '需手动更新') }}
                  </button>
                  <span v-if="!upd.can_apply" class="muted">当前是 {{ upd.platform }}，一键更新只支持 Linux (amd64) 容器</span>
                  <span v-else-if="updSize" class="muted">安装包 {{ updSize }} · 下载后先校验再替换</span>
                </div>
              </div>
              <div v-else class="upd-state ok">已是最新版本</div>
            </template>
            <div v-else class="upd-state muted">点「检查更新」看看有没有新版</div>

            <div v-if="progressVisible" class="upd-progress"
                 :class="{ done: liveProgress?.stage === 'done' || liveProgress?.stage === 'checked', bad: liveProgress?.stage === 'error' }">
              <div class="upd-progress-head">
                <span role="status" aria-live="polite">
                  <i class="upd-stage-dot" aria-hidden="true"></i>{{ progressMessage }}
                </span>
                <b class="num">{{ progressPercent }}%</b>
              </div>
              <div class="upd-progress-track" role="progressbar" aria-label="更新进度"
                   aria-valuemin="0" aria-valuemax="100" :aria-valuenow="progressPercent"
                   :aria-valuetext="progressMessage + '，' + progressPercent + '%'">
                <i :style="{ width: progressPercent + '%' }"></i>
              </div>
              <div v-if="liveProgress?.total_bytes" class="upd-progress-meta">
                已下载 {{ fmtBytes(liveProgress.downloaded_bytes) }} / {{ fmtBytes(liveProgress.total_bytes) }}
              </div>
            </div>

            <details class="upd-cfg" :open="cfgOpen" @toggle="cfgOpen = $event.target.open">
              <summary>更新源：{{ upd?.repo || '默认仓库' }}（私有仓库需要 Token）</summary>
              <div class="upd-cfg-body">
                <label>仓库</label>
                <input class="input" v-model="cfg.repo" placeholder="owner/name" />
                <label>GitHub Token</label>
                <input class="input" type="password" v-model="cfg.token" autocomplete="off"
                       :placeholder="upd?.has_token ? '已保存，留空表示不修改' : '私有仓库必填；只读权限即可'" />
                <label>代理（容器连不上 GitHub 时填）</label>
                <input class="input" v-model="cfg.proxy" placeholder="如 http://10.10.10.2:7890，留空则不用代理" />
                <div class="upd-cfg-ops">
                  <button class="btn btn-sm" :disabled="cfgBusy" @click="saveCfg">保存</button>
                  <button v-if="upd?.has_token" class="btn btn-sm btn-ghost" :disabled="cfgBusy" @click="clearToken">清除 Token</button>
                  <span class="muted">{{ cfgMsg }}</span>
                </div>
                <div class="muted upd-cfg-hint">Token 只存在本机数据库，不会出现在导出备份里；建议用只读、仅此仓库权限的细粒度 Token。代理留空即关闭，也支持 socks5://。</div>
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

<script setup>
import { ref, onMounted, onUnmounted, nextTick } from 'vue'
import { api } from '../api'
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

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'

const props = defineProps({
  src: { type: String, required: true },
  alt: { type: String, default: '订单截图' }
})
const emit = defineEmits(['close'])

// 全屏看图：默认铺满可视区，支持滚轮/双指/按钮缩放与拖动，
// 缩放后单指(或鼠标)拖动平移，双击在 1x 与 2x 之间切换。
const scale = ref(1)
// 关闭时先播完退场动画再真的卸载：不然点「关闭」画面是瞬间消失的，
// 和弹窗、抽屉那几处「有来有回」的手感不一致。
const closing = ref(false)
let closeTimer = 0
function requestClose() {
  if (closing.value) return
  closing.value = true
  clearTimeout(closeTimer)
  closeTimer = setTimeout(() => emit('close'), 180)
}
const tx = ref(0)
const ty = ref(0)
const loading = ref(true)
const failed = ref(false)

const MIN = 1
const MAX = 6

const transform = computed(() => `translate(${tx.value}px, ${ty.value}px) scale(${scale.value})`)
const zoomed = computed(() => scale.value > 1.01)

function clamp(v, lo, hi) { return Math.min(hi, Math.max(lo, v)) }

function applyScale(next) {
  const s = clamp(next, MIN, MAX)
  scale.value = s
  if (s <= MIN) { tx.value = 0; ty.value = 0 }
}

function zoomBy(factor) { applyScale(scale.value * factor) }
function reset() { scale.value = 1; tx.value = 0; ty.value = 0 }

function onWheel(e) {
  e.preventDefault()
  zoomBy(e.deltaY < 0 ? 1.12 : 1 / 1.12)
}

function onDblClick() {
  if (zoomed.value) reset()
  else applyScale(2)
}

// ---- 拖动（鼠标 / 单指）----
let dragging = false
let startX = 0, startY = 0, startTx = 0, startTy = 0

function dragStart(x, y) {
  if (!zoomed.value) return
  dragging = true
  startX = x; startY = y
  startTx = tx.value; startTy = ty.value
}
function dragMove(x, y) {
  if (!dragging) return
  tx.value = startTx + (x - startX)
  ty.value = startTy + (y - startY)
}
function dragEnd() { dragging = false }

function onMouseDown(e) { dragStart(e.clientX, e.clientY) }
function onMouseMove(e) { dragMove(e.clientX, e.clientY) }
function onMouseUp() { dragEnd() }

// ---- 双指缩放 ----
let pinchBase = 0
let pinchScale = 1
function touchDist(t) {
  const dx = t[0].clientX - t[1].clientX
  const dy = t[0].clientY - t[1].clientY
  return Math.hypot(dx, dy)
}
function onTouchStart(e) {
  if (e.touches.length === 2) {
    pinchBase = touchDist(e.touches)
    pinchScale = scale.value
    dragging = false
  } else if (e.touches.length === 1) {
    dragStart(e.touches[0].clientX, e.touches[0].clientY)
  }
}
function onTouchMove(e) {
  if (e.touches.length === 2) {
    e.preventDefault()
    if (pinchBase > 0) applyScale(pinchScale * (touchDist(e.touches) / pinchBase))
  } else if (e.touches.length === 1) {
    dragMove(e.touches[0].clientX, e.touches[0].clientY)
  }
}
function onTouchEnd(e) {
  if (e.touches.length === 0) dragEnd()
}

function onKey(e) {
  if (e.key === 'Escape') { e.preventDefault(); requestClose() }
  else if (e.key === '+' || e.key === '=') { e.preventDefault(); zoomBy(1.2) }
  else if (e.key === '-') { e.preventDefault(); zoomBy(1 / 1.2) }
  else if (e.key === '0') { e.preventDefault(); reset() }
}

onMounted(() => {
  document.body.classList.add('overlay-open')
  window.addEventListener('keydown', onKey, true)
  window.addEventListener('mousemove', onMouseMove)
  window.addEventListener('mouseup', onMouseUp)
})
onUnmounted(() => {
  document.body.classList.remove('overlay-open')
  window.removeEventListener('keydown', onKey, true)
  clearTimeout(closeTimer)
  window.removeEventListener('mousemove', onMouseMove)
  window.removeEventListener('mouseup', onMouseUp)
})
</script>

<template>
  <Teleport to="body">
    <div class="iv-mask" :class="{ 'is-closing': closing }" @click.self="requestClose" @wheel.prevent="onWheel"
         @touchstart.passive="onTouchStart" @touchmove="onTouchMove" @touchend.passive="onTouchEnd">
      <div class="iv-stage" :class="{ zoomed }" @dblclick="onDblClick"
           @mousedown.prevent="onMouseDown" @touchstart.prevent="onTouchStart"
           @touchmove.prevent="onTouchMove" @touchend.passive="onTouchEnd">
        <div v-if="loading && !failed" class="iv-loading">
          <span class="spin"></span>
          <span>加载中…</span>
        </div>
        <div v-if="failed" class="iv-failed">
          图片加载失败
          <button class="btn btn-sm btn-quiet" @click="requestClose">关闭</button>
        </div>
        <img :src="src" :alt="alt" class="iv-img" :style="{ transform }"
             :class="{ hidden: loading || failed }"
             @load="loading = false" @error="loading = false; failed = true" />
      </div>

      <div class="iv-bar" @click.stop>
        <button class="iv-btn" @click="zoomBy(1 / 1.2)" aria-label="缩小" :disabled="!zoomed">−</button>
        <span class="iv-scale">{{ Math.round(scale * 100) }}%</span>
        <button class="iv-btn" @click="zoomBy(1.2)" aria-label="放大" :disabled="scale >= MAX">＋</button>
        <button class="iv-btn iv-reset" @click="reset" aria-label="还原">还原</button>
      </div>

      <button class="iv-close" @click="requestClose" aria-label="关闭查看">
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor"
             stroke-width="2.2" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18" /></svg>
      </button>

      <div class="iv-hint">滚轮 / 双指缩放 · 双击放大 · Esc 关闭</div>
    </div>
  </Teleport>
</template>

<script setup>
/**
 * 问号说明图标。
 * 桌面：鼠标移入即显示，移出收起；触屏：点一下切换，点别处收起。
 * 浮层用 Teleport 挂到 body 并做 fixed 定位——卡片和弹窗普遍带 overflow:hidden，
 * 挂在原地会被裁掉；同时按可视区边界做夹取，窄屏不会溢出屏幕。
 */
import { ref, nextTick, onBeforeUnmount, watch } from 'vue'

const props = defineProps({
  text: { type: String, required: true },
  // 浮层最大宽度（窄屏会再按视口宽度收缩）
  width: { type: Number, default: 300 }
})

const btn = ref(null)
const tip = ref(null)
const open = ref(false)
// ready 用于先把浮层放在屏幕外量尺寸，量完再定位，避免出现一帧错位
const pos = ref({ top: -9999, left: -9999, arrow: 0, above: false, ready: false })

let hideTimer = null

// 只有真正能悬停的设备（鼠标/触控板）才走 hover，触屏走点击，避免手机上「点一下闪一下」
const canHover = typeof window !== 'undefined' &&
  typeof window.matchMedia === 'function' &&
  window.matchMedia('(hover: hover) and (pointer: fine)').matches

function clearHide() {
  if (hideTimer) { clearTimeout(hideTimer); hideTimer = null }
}

async function show() {
  clearHide()
  if (open.value) return
  open.value = true
  pos.value = { top: -9999, left: -9999, arrow: 0, above: false, ready: false }
  await nextTick()
  place()
}

function hide() {
  clearHide()
  open.value = false
}

function scheduleHide() {
  clearHide()
  // 留一点缓冲，鼠标从图标移向浮层时不会闪
  hideTimer = setTimeout(() => { open.value = false }, 90)
}

function place() {
  const b = btn.value
  const t = tip.value
  if (!b || !t) return
  const br = b.getBoundingClientRect()
  const tr = t.getBoundingClientRect()
  const gap = 9
  const vw = window.innerWidth
  const vh = window.innerHeight
  const maxLeft = Math.max(8, vw - tr.width - 8)
  let left = br.left + br.width / 2 - tr.width / 2
  left = Math.min(Math.max(left, 8), maxLeft)
  let above = false
  let top = br.bottom + gap
  if (top + tr.height > vh - 8) {
    const up = br.top - gap - tr.height
    if (up >= 8) { top = up; above = true }
    else top = Math.max(8, vh - tr.height - 8)
  }
  // 箭头始终指向问号图标的中心；浮层被夹取后箭头跟着偏移
  const arrow = Math.min(Math.max(br.left + br.width / 2 - left, 14), tr.width - 14)
  pos.value = { top, left, arrow, above, ready: true }
}

function toggle() {
  if (open.value) hide()
  else show()
}

function onDocPointer(e) {
  if (!open.value) return
  if (btn.value && btn.value.contains(e.target)) return
  hide()
}
function onDocKey(e) {
  if (e.key === 'Escape') hide()
}
function onViewport() {
  if (open.value) place()
}

watch(open, (v) => {
  if (typeof window === 'undefined') return
  if (v) {
    window.addEventListener('scroll', onViewport, true)
    window.addEventListener('resize', onViewport)
    document.addEventListener('pointerdown', onDocPointer, true)
    document.addEventListener('keydown', onDocKey)
  } else {
    window.removeEventListener('scroll', onViewport, true)
    window.removeEventListener('resize', onViewport)
    document.removeEventListener('pointerdown', onDocPointer, true)
    document.removeEventListener('keydown', onDocKey)
  }
})

onBeforeUnmount(() => {
  clearHide()
  window.removeEventListener('scroll', onViewport, true)
  window.removeEventListener('resize', onViewport)
  document.removeEventListener('pointerdown', onDocPointer, true)
  document.removeEventListener('keydown', onDocKey)
})
</script>

<template>
  <!-- 用 span + role=button 而不是 <button>：这个图标常放在 <label> 里，
       <button> 属于 labelable 元素，会被 <label> 当成关联控件，点标签文字也会弹说明。 -->
  <span
    ref="btn"
    class="help-tip"
    role="button"
    tabindex="0"
    :aria-label="'说明：' + text"
    :aria-expanded="open"
    @click.stop="toggle"
    @keydown.stop
    @keydown.enter.prevent="toggle"
    @keydown.space.prevent="toggle"
    @keyup.stop
    @mouseenter="canHover && show()"
    @mouseleave="canHover && scheduleHide()"
  >?</span>

  <Teleport to="body">
    <div
      v-if="open"
      ref="tip"
      class="help-pop"
      :class="{ 'is-visible': pos.ready, 'is-above': pos.above }"
      role="tooltip"
      :style="{
        top: pos.top + 'px',
        left: pos.left + 'px',
        width: 'min(' + width + 'px, calc(100vw - 24px))',
        '--arrow-x': pos.arrow + 'px'
      }"
    >{{ text }}</div>
  </Teleport>
</template>

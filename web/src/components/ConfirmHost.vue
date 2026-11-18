<script setup>
import { ref, watch, nextTick } from 'vue'
import { currentConfirm, settleConfirm } from '../confirm'
import { maskCloser } from '../maskClose'
// 遮罩点击守卫：在输入框里拖选文字划出弹窗松手时，不会误关弹窗
const mask = maskCloser(() => cancel)


// 全局唯一的确认框宿主：只渲染队首，保证同一时刻只有一个确认弹窗
const item = currentConfirm
const cancelBtn = ref(null)
const confirmBtn = ref(null)
const panelEl = ref(null)
let lastFocus = null

// 移动端 sheet 的「下拉关闭」：与录单抽屉保持同一套手感与阈值。
// 危险操作误下滑等同于「取消」，语义安全；关闭前清掉内联 transform，
// 让 Vue Transition 的退场动画能完整接管（否则内联样式会压过 leave-to）。
let dragY = null
let dragTop = 0
function onHeadDown(e) {
  if (window.innerWidth > 600) return
  const el = panelEl.value
  if (!el) return
  const y = e.touches[0].clientY
  // 只从手柄 / 标题区起手，按钮区域下拉不接管，避免误触「取消」
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
  // 与录单抽屉 / 回收站统一：跟手位移 > 64px（约手指下拉 116px）即关闭
  const close = dragTop > 64
  dragY = null
  const el = panelEl.value
  if (!el) return
  if (close) {
    el.style.transition = ''
    el.style.transform = ''
    cancel()
    return
  }
  // 先给过渡再清位移，回弹才生效
  el.style.transition = 'transform var(--dur-2) var(--ease)'
  el.style.transform = ''
  setTimeout(() => { if (panelEl.value) panelEl.value.style.transition = '' }, 260)
}

// 打开时把焦点放进面板（危险操作默认落在「取消」，避免回车误删）；
// 关闭时归还给触发元素，避免键盘用户焦点丢到 body
watch(() => item.value?.id, async (id) => {
  if (!id) {
    try { lastFocus?.focus?.() } catch (e) { /* ignore */ }
    lastFocus = null
    return
  }
  lastFocus = document.activeElement
  await nextTick()
  const el = item.value?.danger ? cancelBtn.value : confirmBtn.value
  el?.focus?.()
})

function cancel() { if (item.value) settleConfirm(item.value.id, false) }
function ok() { if (item.value) settleConfirm(item.value.id, true) }

function onKey(e) {
  if (!item.value) return
  if (e.key === 'Escape') {
    e.preventDefault(); e.stopPropagation(); cancel(); return
  }
  if (e.key === 'Tab') {
    const a = cancelBtn.value, b = confirmBtn.value
    if (!a || !b) return
    if (e.shiftKey && document.activeElement === a) { e.preventDefault(); b.focus() }
    else if (!e.shiftKey && document.activeElement === b) { e.preventDefault(); a.focus() }
    return
  }
  if (e.key === 'Enter') {
    // 焦点在按钮上时交给原生点击，避免一次回车结算两次
    const ae = document.activeElement
    if (ae === cancelBtn.value || ae === confirmBtn.value) return
    e.preventDefault(); cancel()
  }
}
</script>

<template>
  <Transition name="modal">
    <div v-if="item" class="confirm-mask" @pointerdown="mask.onPointerDown" @click="mask.onClick" @keydown="onKey">
      <div ref="panelEl" class="confirm-panel" role="alertdialog" aria-modal="true" aria-labelledby="xy-confirm-title"
           @touchstart.passive="onHeadDown" @touchmove="onHeadMove" @touchend.passive="onHeadEnd">
        <div class="confirm-head">
          <span class="confirm-icon" :class="{ danger: item.danger }" aria-hidden="true">
            <svg v-if="item.danger" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M10.3 3.6 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.6a2 2 0 0 0-3.4 0z" />
              <line x1="12" y1="9" x2="12" y2="13" /><line x1="12" y1="17" x2="12.01" y2="17" />
            </svg>
            <svg v-else width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <circle cx="12" cy="12" r="9" /><path d="M9.5 9a2.5 2.5 0 0 1 5 0c0 1.8-2.5 2-2.5 3.5" />
              <line x1="12" y1="17" x2="12.01" y2="17" />
            </svg>
          </span>
          <div class="confirm-text">
            <h3 id="xy-confirm-title" class="confirm-title">{{ item.title }}</h3>
            <p v-if="item.message" class="confirm-message">{{ item.message }}</p>
            <p v-for="(d, i) in item.detail" :key="i" class="confirm-detail">{{ d }}</p>
          </div>
        </div>
        <div class="confirm-actions">
          <button ref="cancelBtn" class="btn btn-quiet" @click="cancel">{{ item.cancelText }}</button>
          <button ref="confirmBtn" class="btn" :class="item.danger ? 'btn-danger-solid' : 'btn-primary'" @click="ok">{{ item.confirmText }}</button>
        </div>
      </div>
    </div>
  </Transition>
</template>

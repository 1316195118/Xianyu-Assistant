<script setup>
import { ref, onMounted, onUnmounted, nextTick } from 'vue'
import { api } from '../api'
import { maskCloser } from '../maskClose'
import { confirm } from '../confirm'
// 遮罩点击守卫：在输入框里拖选文字划出弹窗松手时，不会误关弹窗
const mask = maskCloser(() => close)


const emit = defineEmits(['notify', 'refresh', 'close'])
const ops = ref([])
const loading = ref(false)
const panelEl = ref(null)
const bodyEl = ref(null)
const closeBtn = ref(null)
let lastFocus = null

async function load() {
  loading.value = true
  try { ops.value = (await api.undoOps()).items || [] }
  catch (e) { ops.value = []; emit('notify', '回收站加载失败：' + e.message, true) }
  finally { loading.value = false }
}

// ---------- 关闭方式与焦点 ----------
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
  load()
})
onUnmounted(() => {
  window.removeEventListener('keydown', onKey, true)
  document.body.classList.remove('overlay-open')
  try { lastFocus?.focus?.() } catch (e) { /* ignore */ }
})

// ---------- 移动端底部 sheet：下拉关闭（与录单抽屉同一套手感） ----------
let dragY = null
let dragTop = 0
function onHeadDown(e) {
  if (window.innerWidth > 600) return
  const el = panelEl.value
  if (!el) return
  // 列表还没滚到顶时不接管手势，避免与内容滚动冲突（与录单抽屉一致）
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
  // 跟手位移超过 64px（约等于手指下拉 116px）即关闭；三处抽屉统一阈值
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

const acting = ref('')
async function restore(o) {
  if (acting.value) return
  acting.value = 'restore:' + o.token
  try {
    await api.undo(o.token)
    emit('notify', '已恢复：' + o.label)
    emit('refresh')
    await load()
  } catch (e) { emit('notify', '恢复失败：' + e.message, true) }
  finally { acting.value = '' }
}
async function discard(o) {
  if (acting.value) return
  const ok = await confirm({
    title: '彻底删除撤销记录',
    message: '删除后这条撤销记录就无法再恢复了。',
    confirmText: '删除', danger: true
  })
  if (!ok) return
  acting.value = 'discard:' + o.token
  try { await api.discardUndo(o.token); emit('notify', '已彻底删除该撤销记录'); await load() }
  catch (e) { emit('notify', '删除失败：' + e.message, true) }
  finally { acting.value = '' }
}
const fmt = (s) => String(s || '').replace('T', ' ').slice(0, 16)
</script>

<template>
  <Teleport to="body">
  <div class="modal-mask" @pointerdown="mask.onPointerDown" @click="mask.onClick">
    <div ref="panelEl" class="modal recycle-modal" role="dialog" aria-modal="true" aria-labelledby="xy-recycle-title"
         @touchstart.passive="onHeadDown" @touchmove="onHeadMove" @touchend.passive="onHeadEnd">
      <div class="modal-head">
        <h3 id="xy-recycle-title">回收站</h3>
        <button ref="closeBtn" class="x" @click="close" aria-label="关闭回收站">×</button>
      </div>

      <div ref="bodyEl" class="modal-body">
        <div class="muted recycle-desc">最近 7 天的可撤销操作，随时可恢复</div>
        <div v-if="loading" class="skel-list">
          <div v-for="i in 3" :key="i" class="skel-row"><span class="skeleton skel-line w70"></span></div>
        </div>
        <div v-else-if="!ops.length" class="empty empty-compact">
          <div class="empty-badge"><svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M5 6l1 14h12l1-14"/></svg></div>
          <div class="empty-title">回收站是空的</div>
          <div class="empty-sub">删除记录或覆盖导入后，7 天内可在这里恢复</div>
        </div>
        <div v-else class="undo-list">
          <div v-for="o in ops" :key="o.token" class="undo-item">
            <span class="undo-badge" :class="{ used: o.consumed }">{{ o.consumed ? '已使用' : '可恢复' }}</span>
            <div class="undo-main">
              <b>{{ o.label }}</b>
              <small class="muted">{{ fmt(o.created_at) }} · {{ Math.round((o.size || 0) / 1024) }} KB</small>
            </div>
            <button v-if="!o.consumed" class="btn btn-sm btn-primary" :disabled="!!acting" @click="restore(o)">
              <span v-if="acting === 'restore:' + o.token" class="spin"></span>恢复
            </button>
            <button class="btn btn-sm btn-ghost btn-danger" :disabled="!!acting" @click="discard(o)">
              <span v-if="acting === 'discard:' + o.token" class="spin"></span>删除
            </button>
          </div>
        </div>
      </div>

      <div class="modal-foot">
        <span class="muted foot-hint">共 {{ ops.length }} 条撤销记录</span>
        <button class="btn btn-sm" @click="load">刷新</button>
        <button class="btn btn-quiet" @click="close">关闭</button>
      </div>
    </div>
  </div>
  </Teleport>
</template>

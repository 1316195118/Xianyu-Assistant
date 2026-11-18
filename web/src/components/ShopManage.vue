<script setup>
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { api } from '../api'
import { shops, activeId, loadShops, switchShop } from '../shopStore'
import { confirm } from '../confirm'
import { maskCloser } from '../maskClose'
// 遮罩点击守卫：在输入框里拖选文字划出弹窗松手时，不会误关弹窗
const mask = maskCloser(() => close)


const emit = defineEmits(['close', 'notify'])

const COLORS = ['#f5b400', '#2f6bff', '#1a9c6b', '#e0492e', '#8b5cf6', '#ec4899']

const panelEl = ref(null)
const bodyEl = ref(null)
const closeBtn = ref(null)
let lastFocus = null

const editingId = ref(0)
const editName = ref('')
const editColor = ref('#f5b400')
const editPassword = ref('')   // 留空=不改动；填写=设置新密码
const creating = ref(false)
const newName = ref('')
const newColor = ref('#2f6bff')
const busy = ref(false)
const err = ref('')

const total = computed(() => shops.value.length)

function close() { emit('close') }

function startEdit(s) {
  editingId.value = s.id
  editName.value = s.name
  editColor.value = s.color
  editPassword.value = ''
  err.value = ''
}
function cancelEdit() { editingId.value = 0; err.value = '' }

async function saveEdit(s) {
  const name = editName.value.trim()
  if (!name || busy.value) return
  busy.value = true
  err.value = ''
  try {
    const payload = { name, color: editColor.value, note: s.note || '', sort: s.sort }
    if (editPassword.value) payload.password = editPassword.value
    await api.updateShop(s.id, payload)
    await loadShops()
    editingId.value = 0
    editPassword.value = ''

    emit('notify', `已保存「${name}」`)
  } catch (e) {
    err.value = e.message || '保存失败'
  } finally {
    busy.value = false
  }
}

// 清除店铺密码：该店回到「无密码直接进入」
async function clearPassword(s) {
  const ok = await confirm({
    title: '清除店铺密码',
    message: `确定清除「${s.name}」的密码？`,
    detail: ['清除后任何人都可以直接进入这个店铺。'],
    confirmText: '清除',
    danger: true
  })
  if (!ok) return
  try {
    await api.updateShop(s.id, { name: s.name, color: s.color, note: s.note || '', sort: s.sort, password: '' })
    await loadShops()
    emit('notify', `已清除「${s.name}」的密码`)
  } catch (e) { err.value = e.message || '清除失败' }
}

async function removeShop(s) {
  const ok = await confirm({
    title: '删除店铺',
    message: `确定删除「${s.name}」？`,
    detail: ['该店铺的记录不会物理删除，只是不再出现在切换器里。'],
    confirmText: '删除',
    danger: true
  })
  if (!ok) return
  busy.value = true
  err.value = ''
  try {
    // 删的是当前店铺时，先把选择切到另一家：否则后续请求会一直带着
    // 已归档的 shop id，列表、统计全部 400，看起来像「店铺全没了」。
    if (s.id === activeId.value) {
      const fallback = shops.value.find((x) => x.id !== s.id)
      if (fallback) switchShop(fallback.id)
    }
    await api.deleteShop(s.id)
    await loadShops()
    emit('notify', `已删除「${s.name}」`)
  } catch (e) {
    err.value = e.message || '删除失败'
  } finally {
    busy.value = false
  }
}

async function submitCreate() {
  const name = newName.value.trim()
  if (!name || busy.value) return
  busy.value = true
  err.value = ''
  try {
    const r = await api.createShop({ name, color: newColor.value })
    await loadShops()
    if (r?.id) switchShop(r.id)
    creating.value = false
    newName.value = ''
    emit('notify', `已创建「${name}」并切换`)
  } catch (e) {
    err.value = e.message || '创建失败'
  } finally {
    busy.value = false
  }
}

function onKey(e) {
  if (e.key === 'Escape') {
    if (editingId.value) { cancelEdit(); return }
    e.preventDefault(); e.stopPropagation(); close()
  }
}

onMounted(async () => {
  lastFocus = document.activeElement
  document.body.classList.add('overlay-open')
  window.addEventListener('keydown', onKey, true)
  await nextTick()
  closeBtn.value?.focus?.()
  try { await loadShops() } catch (e) { err.value = '店铺列表加载失败' }
})
onUnmounted(() => {
  window.removeEventListener('keydown', onKey, true)
  document.body.classList.remove('overlay-open')
  try { lastFocus?.focus?.() } catch (e) { /* ignore */ }
})

// 移动端底部 sheet：与其它二级窗口保持同一套下拉关闭手感
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
  el.style.transition = ''
  el.style.transform = ''
  if (shouldClose) { close(); return }
  el.style.transition = 'transform var(--dur-2) var(--ease)'
  setTimeout(() => { if (panelEl.value) panelEl.value.style.transition = '' }, 260)
}
</script>

<template>
  <Teleport to="body">
  <div class="modal-mask" @pointerdown="mask.onPointerDown" @click="mask.onClick">
    <div ref="panelEl" class="modal shop-manage-modal">
      <div class="modal-head" @touchstart.passive="onHeadDown" @touchmove="onHeadMove" @touchend="onHeadEnd">
        <h3>店铺管理</h3>
        <button ref="closeBtn" class="x" @click="close" aria-label="关闭店铺管理">×</button>
      </div>

      <div ref="bodyEl" class="modal-body">
        <p class="muted sm-desc">
          共 {{ total }} 个店铺，数据互相独立。切换在顶栏进行，这里只做管理。
        </p>

        <div v-if="err" class="tip err sm-err">{{ err }}</div>

        <div class="sm-list">
          <div v-for="s in shops" :key="s.id" class="sm-item" :class="{ editing: editingId === s.id }">
            <template v-if="editingId !== s.id">
              <i class="sm-dot" :style="{ background: s.color }" aria-hidden="true"></i>
              <span class="sm-name">{{ s.name }}</span>
              <span v-if="s.id === activeId" class="sm-badge">当前</span>
              <span v-if="s.has_password" class="sm-lock" title="已设置店铺密码">
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"
                     stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="10" width="16" height="10" rx="2" /><path d="M8 10V7a4 4 0 0 1 8 0v3" /></svg>
              </span>
              <div class="sm-actions">
                <button class="btn btn-sm btn-quiet" @click="startEdit(s)">编辑</button>
                <button class="btn btn-sm btn-quiet sm-danger" :disabled="busy" @click="removeShop(s)">删除</button>
              </div>
            </template>

            <form v-else class="sm-form" @submit.prevent="saveEdit(s)">
              <input v-model="editName" class="input sm-input" maxlength="30" placeholder="店铺名称" />
              <div class="sm-colors">
                <button v-for="c in COLORS" :key="c" type="button" class="sm-color"
                        :class="{ on: editColor === c }" :style="{ background: c }"
                        :aria-label="'选择颜色 ' + c" @click="editColor = c"></button>
              </div>
              <div class="sm-pw-row">
                <input v-model="editPassword" type="password" class="input sm-input"
                       :placeholder="s.has_password ? '设置新密码（留空则不变）' : '设置店铺密码（可选）'"
                       autocomplete="new-password" />
                <button v-if="s.has_password" type="button" class="btn btn-sm btn-quiet sm-danger"
                        :disabled="busy" @click="clearPassword(s)">清除</button>
              </div>
              <div class="sm-form-actions">
                <button type="button" class="btn btn-sm btn-quiet" @click="cancelEdit">取消</button>
                <button type="submit" class="btn btn-sm btn-primary" :disabled="!editName.trim() || busy">
                  {{ busy ? '保存中…' : '保存' }}
                </button>
              </div>
            </form>
          </div>

          <div v-if="!shops.length" class="empty sm-empty">
            <div class="empty-title">还没有店铺</div>
            <div class="empty-sub muted">新建一个开始记账</div>
          </div>
        </div>

        <form v-if="creating" class="sm-form sm-create" @submit.prevent="submitCreate">
          <input v-model="newName" class="input sm-input" maxlength="30" placeholder="店铺名称，如「小号 A」" />
          <div class="sm-colors">
            <button v-for="c in COLORS" :key="c" type="button" class="sm-color"
                    :class="{ on: newColor === c }" :style="{ background: c }"
                    :aria-label="'选择颜色 ' + c" @click="newColor = c"></button>
          </div>
          <div class="sm-form-actions">
            <button type="button" class="btn btn-sm btn-quiet" @click="creating = false">取消</button>
            <button type="submit" class="btn btn-sm btn-primary" :disabled="!newName.trim() || busy">
              {{ busy ? '创建中…' : '创建并切换' }}
            </button>
          </div>
        </form>
        <button v-else class="btn btn-quiet sm-new" @click="creating = true">＋ 新建店铺</button>
      </div>

      <div class="modal-foot">
        <span class="foot-hint muted">删除只是归档，记录仍保留在数据库中</span>
        <button class="btn btn-quiet" @click="close">关闭</button>
      </div>
    </div>
  </div>
  </Teleport>
</template>

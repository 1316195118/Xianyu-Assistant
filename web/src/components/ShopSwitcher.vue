<script setup>
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { shops, activeId, activeShop, loadShops, switchShop, createShop, globalSession } from '../shopStore'
import { markShopPicked } from '../shopId'

const emit = defineEmits(['changed', 'notify'])

const open = ref(false)
const creating = ref(false)
const newName = ref('')
const newColor = ref('#2f6bff')
const busy = ref(false)
const rootEl = ref(null)
const nameEl = ref(null)

const COLORS = ['#f5b400', '#2f6bff', '#1a9c6b', '#e0492e', '#8b5cf6', '#ec4899']

const label = computed(() => activeShop.value?.name || '我的闲鱼')
const dotColor = computed(() => activeShop.value?.color || '#f5b400')

function toggle() {
  open.value = !open.value
  if (open.value) creating.value = false
}

function pick(s) {
  if (s.id === activeId.value) { open.value = false; return }
  // 用店铺密码登录的账号被锁在这家店，切过去只会看到同一份数据
  if (!globalSession.value) {
    open.value = false
    emit('notify', '当前账号只能访问本店铺，切换请先退出并用全局密码登录')
    return
  }
  switchShop(s.id)
  markShopPicked()
  open.value = false
  emit('changed', s)
}

async function submitCreate() {
  const name = newName.value.trim()
  if (!name || busy.value) return
  busy.value = true
  try {
    const r = await createShop(name, newColor.value)
    newName.value = ''
    creating.value = false
    open.value = false
    emit('changed', shops.value.find((s) => s.id === (r?.id || activeId.value)) || null)
    emit('notify', `已创建店铺「${name}」`)
  } catch (e) {
    emit('notify', e.message || '创建店铺失败')
  } finally {
    busy.value = false
  }
}

function startCreate() {
  creating.value = true
  nextTick(() => nameEl.value?.focus())
}

function onDocClick(e) {
  if (!open.value) return
  if (rootEl.value && !rootEl.value.contains(e.target)) open.value = false
}
function onKey(e) {
  if (e.key === 'Escape' && open.value) { open.value = false }
}

onMounted(() => {
  document.addEventListener('click', onDocClick, true)
  document.addEventListener('keydown', onKey)
  if (!shops.value.length) loadShops()
})
onUnmounted(() => {
  document.removeEventListener('click', onDocClick, true)
  document.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div ref="rootEl" class="shop-switch">
    <button class="shop-btn" :class="{ open }" @click.stop="toggle"
            :title="`当前店铺：${label}（点击切换）`" :aria-expanded="open" aria-haspopup="listbox">
      <i class="shop-dot" :style="{ background: dotColor }" aria-hidden="true"></i>
      <span class="shop-name">{{ label }}</span>
      <svg class="shop-chev" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor"
           stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6" /></svg>
    </button>

    <Transition name="shop-pop">
      <div v-if="open" class="shop-menu" role="listbox" @click.stop>
        <div class="shop-menu-head">切换店铺</div>
        <button v-for="s in shops" :key="s.id" class="shop-item" :class="{ on: s.id === activeId }"
                role="option" :aria-selected="s.id === activeId" @click="pick(s)">
          <i class="shop-dot" :style="{ background: s.color }" aria-hidden="true"></i>
          <span class="shop-item-name">{{ s.name }}</span>
          <svg v-if="s.id === activeId" class="shop-tick" width="14" height="14" viewBox="0 0 24 24"
               fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round">
            <path d="M20 6L9 17l-5-5" /></svg>
        </button>

        <div v-if="!shops.length" class="shop-empty muted">正在载入店铺…</div>

        <div class="shop-sep"></div>

        <form v-if="creating" class="shop-create" @submit.prevent="submitCreate">
          <input ref="nameEl" v-model="newName" class="input shop-input" maxlength="30"
                 placeholder="店铺名称，如「小号 A」" />
          <div class="shop-colors">
            <button v-for="c in COLORS" :key="c" type="button" class="shop-color"
                    :class="{ on: newColor === c }" :style="{ background: c }"
                    :aria-label="'选择颜色 ' + c" @click="newColor = c"></button>
          </div>
          <div class="shop-create-row">
            <button type="button" class="btn btn-sm btn-quiet" @click="creating = false">取消</button>
            <button type="submit" class="btn btn-sm btn-primary" :disabled="!newName.trim() || busy">
              {{ busy ? '创建中…' : '创建并切换' }}
            </button>
          </div>
        </form>
        <button v-else class="shop-item shop-new" @click="startCreate">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor"
               stroke-width="2.4" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" /></svg>
          新建店铺
        </button>
      </div>
    </Transition>
  </div>
</template>

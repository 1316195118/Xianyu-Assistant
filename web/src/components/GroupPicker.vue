<script setup>
import { ref, computed, onMounted } from 'vue'
import { api } from '../api'
import { maskCloser } from '../maskClose'
// 遮罩点击守卫：在输入框里拖选文字划出弹窗松手时，不会误关弹窗
const mask = maskCloser(() => emit('close'))


// 明细里「加入联单」的小面板：选已有联单，或者直接新建一张把这几条挂进去。
const props = defineProps({
  itemIds: { type: Array, default: () => [] },
  // 已在某些联单里的记录 id -> 联单标题数组，用来标「已在该联单」
  membership: { type: Object, default: () => ({}) }
})
const emit = defineEmits(['close', 'notify', 'done'])

const groups = ref([])
const loading = ref(true)
const q = ref('')
const busy = ref(0)          // >0 = 正在加入某张；-1 = 正在新建
const newTitle = ref('')

onMounted(load)

async function load() {
  loading.value = true
  try {
    groups.value = (await api.groupsBrief()).items || []
  } catch (e) {
    emit('notify', '联单列表加载失败：' + (e.message || ''), true)
  } finally {
    loading.value = false
  }
}

const filtered = computed(() => {
  const kw = q.value.trim().toLowerCase()
  const list = [...groups.value].sort((a, b) => b.id - a.id)
  if (!kw) return list
  return list.filter(g => String(g.title || '').toLowerCase().includes(kw))
})
const count = computed(() => props.itemIds.length)

// 这些记录全都已经在某张联单里
const allIn = (g) => props.itemIds.length > 0 && props.itemIds.every(id => (props.membership[id] || []).includes(g.title))
const someIn = (g) => props.itemIds.some(id => (props.membership[id] || []).includes(g.title))

async function addTo(g) {
  if (!count.value || busy.value) return
  if (g.settled_at) {
    emit('notify', `「${g.title || '未命名联单'}」已完结，请先在「联单」页重新开启`, true)
    return
  }
  if (allIn(g)) {
    emit('notify', `这些记录已经在「${g.title}」里了`)
    return
  }
  busy.value = g.id
  try {
    const r = await api.addGroupItems(g.id, props.itemIds)
    if (r.added > 0) {
      // 原本「单独完结」的记录会在这里转交给联单，顺手说一声，免得用户以为完结状态丢了
      const tail = r.cleared > 0 ? `，其中 ${r.cleared} 条原为单独完结，已改为按联单结算` : ''
      emit('notify', `已把 ${r.added} 条记录加入「${g.title || '未命名联单'}」` + tail)
    } else {
      emit('notify', `这些记录已经在「${g.title}」里了`)
    }
    emit('done')
    emit('close')
  } catch (e) {
    emit('notify', e.message || '加入失败', true)
  } finally {
    busy.value = 0
  }
}

async function createAndAdd() {
  const t = newTitle.value.trim()
  if (!t) { emit('notify', '请先填写联单标题', true); return }
  if (!count.value || busy.value) return
  busy.value = -1
  try {
    const g = await api.createGroup({ title: t, note: '', item_ids: props.itemIds, externals: [], externals_buy: [] })
    emit('notify', `已新建「${g.title || t}」并加入 ${count.value} 条记录`)
    emit('done')
    emit('close')
  } catch (e) {
    emit('notify', e.message || '新建联单失败', true)
  } finally {
    busy.value = 0
  }
}
</script>

<template>
  <Teleport to="body">
    <div class="gp-mask" @pointerdown="mask.onPointerDown" @click="mask.onClick">
      <div class="gp-panel" role="dialog" aria-label="加入联单">
        <div class="gp-head">
          <h3>加入联单</h3>
          <span class="muted">共 {{ count }} 条记录</span>
          <button class="gp-x" aria-label="关闭" @click="emit('close')">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><line x1="6" y1="6" x2="18" y2="18"/><line x1="18" y1="6" x2="6" y2="18"/></svg>
          </button>
        </div>

        <div class="gp-new">
          <input class="input" v-model="newTitle" placeholder="新建一张联单，标题写这里"
                 @keyup.enter="createAndAdd" />
          <button class="btn btn-primary" :disabled="!!busy" @click="createAndAdd">
            {{ busy === -1 ? '创建中…' : '新建并加入' }}
          </button>
        </div>

        <div class="gp-search">
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
          <input class="input" v-model="q" placeholder="搜索已有联单" />
        </div>

        <div class="gp-list">
          <div v-if="loading" class="gp-empty muted">加载中…</div>
          <template v-else>
            <button v-for="g in filtered" :key="g.id" class="gp-row" :class="{ 'is-settled': g.settled_at }"
                    :disabled="!!busy || !!g.settled_at" @click="addTo(g)">
              <span class="gp-dot" :class="{ sealed: g.settled_at }" aria-hidden="true"></span>
              <span class="gp-title">{{ g.title || '未命名联单' }}</span>
              <span v-if="g.settled_at" class="gp-tag gp-tag-settled">已完结</span>
              <span v-else-if="allIn(g)" class="gp-tag">已在其中</span>
              <span v-else-if="someIn(g)" class="gp-tag gp-tag-part">部分已在</span>
              <span class="gp-add">{{ busy === g.id ? '加入中…' : '加入' }}</span>
            </button>
            <div v-if="!filtered.length" class="gp-empty muted">
              {{ q.trim() ? '没有匹配的联单，可以直接在上面新建一张' : '还没有联单，直接在上面新建一张' }}
            </div>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

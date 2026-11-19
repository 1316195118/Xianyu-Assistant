<script setup>
import { ref, reactive, computed, onMounted } from 'vue'
import { api } from '../api'
import { fmtMoney } from '../utils'

// 合并建议：把「标题很像的几张联单」和「同名但还没进联单的散单」列出来，
// 每条都能改标题、单独勾选，最后一起提交。默认一条都不勾，避免手快点掉。
const emit = defineEmits(['close', 'done', 'notify'])

const loading = ref(true)
const err = ref('')
const groupSug = ref([])
const itemSug = ref([])
const skippedSettled = ref(0)
const picked = reactive({})   // key -> bool
const titles = reactive({})   // key -> 用户改过的标题
const busy = ref(false)

const allKeys = computed(() => [...groupSug.value, ...itemSug.value].map((s) => s.key))
const pickedCount = computed(() => allKeys.value.filter((k) => picked[k]).length)
const allPicked = computed(() => allKeys.value.length > 0 && pickedCount.value === allKeys.value.length)

function toggleAll() {
  const on = !allPicked.value
  for (const k of allKeys.value) picked[k] = on
}

function groupCount(s) {
  return s.groups.reduce((n, g) => n + (g.items || 0), 0)
}

async function load() {
  loading.value = true
  err.value = ''
  try {
    const r = await api.groupMergeSuggestions()
    groupSug.value = r.groups || []
    itemSug.value = r.items || []
    skippedSettled.value = r.skipped_settled || 0
    for (const s of [...groupSug.value, ...itemSug.value]) {
      picked[s.key] = false
      titles[s.key] = s.title || ''
    }
  } catch (e) {
    err.value = e.message || '读取合并建议失败'
  } finally {
    loading.value = false
  }
}
onMounted(load)

async function submit() {
  if (busy.value || !pickedCount.value) return
  busy.value = true
  let mergedGroups = 0
  let madeGroups = 0
  let movedItems = 0
  const fails = []
  try {
    for (const s of [...groupSug.value, ...itemSug.value]) {
      if (!picked[s.key]) continue
      const title = (titles[s.key] || '').trim()
      if (!title) {
        fails.push('有一条建议没填标题')
        continue
      }
      try {
        const body = s.kind === 'group'
          ? { kind: 'group', title, group_ids: s.groups.map((g) => g.id) }
          : { kind: 'items', title, item_ids: s.item_ids }
        const r = await api.applyGroupMerge(body)
        movedItems += r.moved || 0
        if (s.kind === 'group') mergedGroups += Math.max(0, (s.groups.length || 0) - 1)
        else madeGroups++
      } catch (e) {
        fails.push(`「${title}」：${e.message || '失败'}`)
      }
    }
    emit('done')
    const parts = []
    if (mergedGroups) parts.push(`合并了 ${mergedGroups} 张联单`)
    if (madeGroups) parts.push(`新建了 ${madeGroups} 张联单`)
    if (movedItems) parts.push(`涉及 ${movedItems} 条记录`)
    if (parts.length) emit('notify', '已' + parts.join('，'))
    if (fails.length) emit('notify', fails[0], true)
    if (!fails.length) emit('close')
    else await load()
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="modal-mask set-sheet" @click.self="emit('close')">
    <div class="modal merge-modal" role="dialog" aria-modal="true" aria-label="合并建议">
      <div class="modal-head">
        <h3>合并建议</h3>
        <button class="x" @click="emit('close')" aria-label="关闭">×</button>
      </div>

      <div class="modal-body">
        <p class="muted merge-intro">
          把同名的单子并成一张联单，账更清楚。下面每条都能改标题、勾选后再提交，没勾的不会动。
        </p>

        <div v-if="err" class="alert-err">{{ err }} <button class="btn btn-sm" @click="load">重试</button></div>
        <div v-else-if="loading" class="merge-loading"><span class="spin"></span>正在找相似联单…</div>

        <template v-else>
          <div v-if="skippedSettled" class="merge-note">
            有 {{ skippedSettled }} 张已完结联单没参与建议 —— 完结是对过账的快照，需要先在列表里重新开启再合并。
          </div>

          <div v-if="groupSug.length" class="merge-toolbar">
            <span class="merge-sec-title">标题很像的联单 · {{ groupSug.length }} 条建议</span>
            <button class="btn btn-sm btn-quiet" @click="toggleAll">{{ allPicked ? '全不选' : '全选' }}</button>
          </div>

          <label v-for="s in groupSug" :key="s.key" class="merge-item" :class="{ on: picked[s.key] }">
            <input type="checkbox" class="merge-check" v-model="picked[s.key]" />
            <div class="merge-body">
              <div class="merge-line">
                <input class="input merge-title" v-model="titles[s.key]" :disabled="!picked[s.key]" aria-label="合并后的联单标题" />
                <span class="merge-score" :title="'标题相似度 ' + s.score + '%'">{{ s.score }}%</span>
              </div>
              <div class="merge-sub">{{ s.reason }} · 共 {{ groupCount(s) }} 条记录</div>
              <div class="merge-chips">
                <span v-for="g in s.groups" :key="g.id" class="merge-chip">
                  {{ g.title || '未命名联单' }}<i>{{ g.items }}</i>
                </span>
              </div>
            </div>
          </label>

          <div v-if="itemSug.length" class="merge-toolbar">
            <span class="merge-sec-title">同名散单 → 新建联单 · {{ itemSug.length }} 条建议</span>
          </div>

          <label v-for="s in itemSug" :key="s.key" class="merge-item" :class="{ on: picked[s.key] }">
            <input type="checkbox" class="merge-check" v-model="picked[s.key]" />
            <div class="merge-body">
              <div class="merge-line">
                <input class="input merge-title" v-model="titles[s.key]" :disabled="!picked[s.key]" aria-label="新建联单标题" />
                <span v-if="s.has_both" class="merge-badge" title="买卖都有，成本能直接对上">买+卖</span>
              </div>
              <div class="merge-sub">{{ s.reason }}</div>
              <div class="merge-chips">
                <span v-for="it in s.items.slice(0, 6)" :key="it.id" class="merge-chip" :class="it.kind">
                  {{ it.product }}<i>{{ it.kind === 'buy' ? '买' : '卖' }} {{ fmtMoney(it.amount) }}</i>
                </span>
                <span v-if="s.items.length > 6" class="merge-chip more">+{{ s.items.length - 6 }}</span>
              </div>
            </div>
          </label>

          <div v-if="!groupSug.length && !itemSug.length" class="empty merge-empty">
            <div class="empty-badge">
              <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>
            </div>
            <div class="empty-title">没有需要合并的</div>
            <div class="empty-sub">联单标题都不重复，也没有同名散单堆着。等有了会自动出现在这里。</div>
          </div>
        </template>
      </div>

      <div class="modal-foot merge-foot">
        <span class="muted merge-picked">已选 {{ pickedCount }} / {{ allKeys.length }}</span>
        <div class="merge-foot-acts">
          <button class="btn btn-ghost" @click="emit('close')">取消</button>
          <button class="btn btn-primary" :disabled="busy || !pickedCount" @click="submit">
            <span v-if="busy" class="spin"></span>提交合并<span v-if="pickedCount" class="merge-btn-n">（{{ pickedCount }}）</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

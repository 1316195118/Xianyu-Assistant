<script setup>
import { ref, computed, watch, nextTick, onBeforeUnmount } from 'vue'

const props = defineProps({
  open: { type: Boolean, default: false },
  commands: { type: Array, default: () => [] }
})
const emit = defineEmits(['close', 'run'])

const q = ref('')
const active = ref(0)
const inputEl = ref(null)
let runTimer = 0

const filtered = computed(() => {
  const s = q.value.trim().toLowerCase()
  if (!s) return props.commands
  return props.commands.filter(c =>
    (c.label + ' ' + (c.desc || '') + ' ' + (c.keywords || '')).toLowerCase().includes(s)
  )
})

watch(() => props.open, async (v) => {
  if (!v) return
  q.value = ''
  active.value = 0
  await nextTick()
  inputEl.value?.focus()
})
watch(filtered, () => {
  if (active.value >= filtered.value.length) active.value = 0
})

function move(d) {
  const n = filtered.value.length
  if (!n) return
  active.value = (active.value + d + n) % n
}
function choose(i) {
  const c = filtered.value[i]
  if (!c) return
  emit('close')
  clearTimeout(runTimer)
  // 先提交关闭与按压反馈，再执行跳转/打开工具。避免命令已经切了页面，
  // 200ms 的遮罩却还盖在新页面上面，看起来像点击后卡了一下。
  runTimer = setTimeout(() => emit('run', c), 24)
}
function onKey(e) {
  if (e.key === 'ArrowDown') { e.preventDefault(); move(1) }
  else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1) }
  else if (e.key === 'Enter') { e.preventDefault(); choose(active.value) }
  else if (e.key === 'Escape') { e.preventDefault(); emit('close') }
}

onBeforeUnmount(() => clearTimeout(runTimer))
</script>

<template>
  <Transition name="cmd">
    <div v-if="open" class="cmd-mask" @click.self="emit('close')" @keydown="onKey">
      <div class="cmd-panel" role="dialog" aria-modal="true" aria-label="命令面板">
        <div class="cmd-search">
          <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><line x1="16.5" y1="16.5" x2="21" y2="21"/></svg>
          <input ref="inputEl" v-model="q" class="cmd-input" placeholder="搜索页面或操作，例如「明细」「深色」…" aria-label="搜索命令" />
          <kbd class="cmd-esc">Esc</kbd>
        </div>
        <div v-if="filtered.length" class="cmd-list">
          <button v-for="(c, i) in filtered" :key="c.id" class="cmd-item" :class="{ on: i === active }"
            @mouseenter="active = i" @click="choose(i)">
            <span class="cmd-ico" aria-hidden="true">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path :d="c.icon" /></svg>
            </span>
            <span class="cmd-main">
              <b>{{ c.label }}</b>
              <small v-if="c.desc">{{ c.desc }}</small>
            </span>
            <span v-if="c.hint" class="cmd-hint">{{ c.hint }}</span>
          </button>
        </div>
        <div v-else class="cmd-empty">没有匹配的命令</div>
        <div class="cmd-foot">
          <span><kbd>↑</kbd><kbd>↓</kbd> 选择</span>
          <span><kbd>Enter</kbd> 执行</span>
          <span><kbd>Esc</kbd> 关闭</span>
        </div>
      </div>
    </div>
  </Transition>
</template>

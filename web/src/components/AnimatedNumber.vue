<script setup>
import { ref, watch, onMounted, onUnmounted } from 'vue'

const props = defineProps({
  value: { type: Number, default: 0 },
  duration: { type: Number, default: 700 },
  // 数值变化时是否让底色闪一下：只滚动数字的话，正负翻转很容易被眼睛漏掉
  flash: { type: Boolean, default: true }
})
const display = ref(0)
const flashDir = ref('')      // '' | 'up' | 'down'
let raf = null
let flashTimer = 0

// 尊重无障碍设置：系统级「减少动效」或设置页强制减少动效时，数字直接跳到终值。
// 用 CSS 的 .001ms 兜底只能干掉 CSS 动画，requestAnimationFrame 的滚动得在这里拦。
function reducedMotion() {
  if (typeof document !== 'undefined' && document.documentElement.getAttribute('data-motion') === 'reduce') return true
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

function animate(to, first) {
  if (reducedMotion()) {
    cancelAnimationFrame(raf)
    display.value = to
    return
  }
  const from = display.value
  // 首次挂载不做闪光：页面刚打开就一片闪烁，反而分不清哪些是「新变化」。
  // 120ms 后移除类名，借 CSS 的过渡把底色淡回去（加类名瞬间上色）。
  if (!first && props.flash && Math.abs(to - from) > 0.005) {
    flashDir.value = to > from ? 'up' : 'down'
    clearTimeout(flashTimer)
    flashTimer = setTimeout(() => { flashDir.value = '' }, 120)
  }
  const start = performance.now()
  const dur = Math.max(1, props.duration)
  cancelAnimationFrame(raf)
  const step = (now) => {
    const t = Math.min(1, (now - start) / dur)
    const eased = 1 - Math.pow(1 - t, 3) // easeOutCubic
    display.value = from + (to - from) * eased
    if (t < 1) raf = requestAnimationFrame(step)
    else display.value = to
  }
  raf = requestAnimationFrame(step)
}
onMounted(() => animate(props.value, true))
watch(() => props.value, (v) => animate(v, false))
onUnmounted(() => { cancelAnimationFrame(raf); clearTimeout(flashTimer) })
</script>

<template>
  <span class="an" :class="flashDir ? 'an-flash-' + flashDir : ''"><slot :value="display">{{ Math.round(display) }}</slot></span>
</template>

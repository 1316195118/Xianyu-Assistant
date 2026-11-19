<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { api } from '../api'
import { fmtMoney } from '../utils'

// 必须随 refreshKey 重拉：本面板的数据全部来自服务端聚合，
// 只在 onMounted 取一次的话，切换店铺后会一直显示上一家店的洞察。
const props = defineProps({ refreshKey: { type: Number, default: 0 } })
const emit = defineEmits(['goto-product'])

const data = ref(null)
const failed = ref(false)
async function load() {
  try {
    data.value = await api.statsExtras()
    failed.value = false
  } catch (e) {
    // 不要清空成 null：那会让整块洞察直接消失，看起来像页面没加载出来。
    // 保留上一次的数据并给出重试入口，切店瞬间的抖动也不至于留白。
    failed.value = true
  }
}
onMounted(load)
watch(() => props.refreshKey, load)

// 后端已按「已完结 + 盈利」过滤，这里再做一道兜底，避免旧缓存/异常数据混入 ¥0.00
const tops = computed(() => (data.value?.top_products || [])
  .filter(p => Number(p.profit) > 0)
  .slice(0, 5))
const maxProfit = computed(() => Math.max(1, ...tops.value.map(p => Math.abs(p.profit || 0))))
const staleItems = computed(() => data.value?.stale_items || [])
const staleThreshold = computed(() => data.value?.stale_threshold || 60)
</script>

<template>
  <section v-if="data || failed" class="card panel insights-panel">
    <div class="panel-head"><h3>经营洞察</h3><span class="muted">近一年入库 · 按商品聚合</span></div>
    <div v-if="failed" class="insight-retry">
      <span>数据加载失败</span>
      <button class="btn btn-sm btn-quiet" @click="load">重试</button>
    </div>
    <div class="insight-metrics">
      <div><span>在库周转天数</span><b class="num">{{ Number(data.turnover_days || 0).toFixed(1) }} 天</b></div>
      <div><span>待售件数</span><b class="num">{{ data.unsold_count || 0 }}</b></div>
    </div>

    <!-- 滞销提醒：在库超阈值未售 -->
    <div v-if="staleItems.length" class="insight-stale">
      <div class="insight-stale-head">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.3 3.6 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.6a2 2 0 0 0-3.4 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
        <b>{{ staleItems.length }} 件在库超 {{ staleThreshold }} 天未售</b>
      </div>
      <div v-for="s in staleItems.slice(0, 5)" :key="s.id" class="insight-stale-row">
        <span class="cell-ellipsis">{{ s.product }}</span>
        <span class="stale-days">{{ s.days }} 天</span>
      </div>
    </div>

    <!-- 只有一条时既没有「排行」可言，那块也显得空；留 0 条时的说明，藏掉 1 条的榜单 -->
    <template v-if="tops.length !== 1">
      <div class="insight-title muted">净利润 Top 5 商品</div>
      <div class="insight-top">
        <div v-for="(p, i) in tops" :key="p.product" class="insight-row insight-row-bar insight-click"
           role="button" tabindex="0" :title="'查看「' + p.product + '」的全部单据'"
           @click="emit('goto-product', { name: p.product, norm: p.norm || '' })"
           @keyup.enter="emit('goto-product', { name: p.product, norm: p.norm || '' })">
          <span class="insight-rank">{{ i + 1 }}</span>
          <span class="cell-ellipsis">
            {{ p.product }}
            <i v-if="p.settled" class="insight-done">已完结</i>
          </span>
          <b class="num" :class="p.profit >= 0 ? 'pos' : 'neg'">{{ fmtMoney(p.profit) }}</b>
          <div class="insight-bar">
            <div class="insight-bar-fill" :class="p.profit >= 0 ? 'pos' : 'neg'" :style="{ width: Math.max(2, Math.abs(p.profit || 0) / maxProfit * 100) + '%' }"></div>
          </div>
        </div>
        <div v-if="!tops.length" class="muted insight-empty">
          暂无已完结且盈利的商品。同步进来的纯卖单没有买进成本，不参与排名；补上买价或关联买进单后才会进榜。
        </div>
      </div>
    </template>
  </section>
</template>

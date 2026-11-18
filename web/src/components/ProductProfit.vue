<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { api } from '../api'
import { fmtMoney } from '../utils'
import { currentShopId } from '../shopId'
import HelpTip from './HelpTip.vue'

const props = defineProps({ refreshKey: { type: Number, default: 0 } })
const emit = defineEmits(['open-product'])

const rows = ref([])
const loading = ref(true)
const tab = ref('top') // top=赚最多 bottom=亏最多

async function load() {
  loading.value = true
  try {
    rows.value = (await api.productProfit()).items || []
  } catch (e) {
    rows.value = []
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => props.refreshKey, load)

// 只列出真的成交过的；亏最多按利润升序，只要利润为负的
const list = computed(() => {
  const sold = rows.value.filter(r => r.sold > 0)
  if (tab.value === 'bottom') {
    return sold.filter(r => r.profit < 0).slice().reverse().slice(0, 8)
  }
  return sold.slice(0, 8)
})
const hasLoss = computed(() => rows.value.some(r => r.sold > 0 && r.profit < 0))
const unlinkedTotal = computed(() => rows.value.reduce((n, r) => n + (r.unlinked || 0), 0))

// 导出直链要带店铺（<a> 带不了 X-Shop-Id），否则在 B 店点导出会拿到默认店的数据
function exportXlsx() {
  const sid = currentShopId()
  location.href = '/api/product-profit/export' + (sid ? '?shop=' + sid : '')
}
</script>

<template>
  <section class="card panel pp-panel">
    <div class="panel-head">
      <h3>商品净利润排行<HelpTip text="按商品名聚合：销售额 = 该商品所有卖单成交价；成本 = 卖单带走的单件成本 + 卖运费 + 服务费。卖单没填单件成本、但所在联单有买进单时，按各卖单成交价占比把买单的买价 + 买运费分摊下来（只用于核算，不改单据）。既没填成本、又不在含买进单的联单里的，成本按 0 计，利润会偏高，单独标出「未挂成本」。代卖、寄卖、赠送这类本来就没有货品成本的，在明细里标一下「无成本」就不会再提示（利润按销售额全额计，这是真实的）。" /></h3>
      <div class="pp-head-right">
        <button class="btn btn-sm btn-quiet" @click="exportXlsx" title="导出成 Excel（一行一个商品 + 合计行）">导出</button>
      <div class="pp-tabs">
        <button :class="{ on: tab === 'top' }" @click="tab = 'top'">赚最多</button>
        <button v-if="hasLoss" :class="{ on: tab === 'bottom' }" @click="tab = 'bottom'">亏最多</button>
      </div>
      </div>
    </div>

    <div v-if="loading" class="pp-list">
      <div v-for="i in 4" :key="'s' + i" class="pp-row"><span class="skeleton skel-line w70"></span></div>
    </div>
    <div v-else-if="!list.length" class="pp-empty muted">
      还没有成交记录，录入或同步订单后这里会按商品排利润。
    </div>
    <div v-else class="pp-list">
      <div v-for="r in list" :key="r.norm || r.product" class="pp-row pp-row-click"
           @click="emit('open-product', { name: r.product, norm: r.norm })" title="点击到明细里看这个商品的所有单据（可批量改/挂成本）">
        <div class="pp-main">
          <div class="pp-name cell-ellipsis" :title="r.product">{{ r.product }}</div>
          <div class="pp-meta muted">
            <span class="pp-base">{{ r.sold }} 单 · 销售额 {{ fmtMoney(r.sales) }} · 成本 {{ fmtMoney(r.cost) }}</span>
            <span v-if="r.stock > 0" class="pp-tag">· 在库 {{ fmtMoney(r.stock) }}</span>
            <span v-if="r.unlinked > 0" class="pp-tag pp-warn" title="这些卖单既没挂买进单，也不在含买进单的联单里，成本只能按 0 计">· {{ r.unlinked }} 单未挂成本</span>
            <span v-if="r.cost_free_count > 0" class="pp-tag pp-free" title="已标记「无成本」：代卖 / 寄卖 / 赠送，利润按销售额全额计">· {{ r.cost_free_count }} 单无成本</span>
          </div>
        </div>
        <div class="pp-num">
          <b class="num" :class="r.profit >= 0 ? 'pos' : 'neg'">{{ fmtMoney(r.profit) }}</b>
          <span class="pp-margin" :class="r.profit >= 0 ? 'pos' : 'neg'">{{ (r.margin || 0).toFixed(1) }}%</span>
          <svg class="pp-go" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </div>
      </div>
    </div>

    <div v-if="!loading && unlinkedTotal > 0" class="pp-tip muted">
      有 {{ unlinkedTotal }} 单卖单没挂成本，利润会偏高 —— 去明细筛「未挂成本」批量挂上。
    </div>
  </section>
</template>

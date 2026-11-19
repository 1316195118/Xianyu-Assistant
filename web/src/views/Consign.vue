<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { api } from '../api'
import { fmtMoney, fmtNum } from '../utils'

// 寄卖账本：别人放在这卖的货，按归属人分开查账。
// 这些单不计入首页任何利润 / 成本口径（后端已按 source=寄卖 排除），只在这里统计。
const props = defineProps({ refreshKey: { type: Number, default: 0 } })
const emit = defineEmits(['notify'])

const data = ref(null)
const err = ref('')
const q = ref('')
const active = ref('')

async function load() {
  try {
    data.value = await api.consignStats()
    err.value = ''
  } catch (e) {
    err.value = e.message || '加载失败'
  }
}
onMounted(load)
watch(() => props.refreshKey, load)

const owners = computed(() => {
  const list = (data.value && data.value.owners) || []
  const k = q.value.trim()
  return k ? list.filter((o) => o.owner.includes(k)) : list
})
const total = computed(() => data.value?.total || { orders: 0, sales: 0, net: 0 })
const shown = computed(() => (active.value ? owners.value.filter((o) => o.owner === active.value) : owners.value))

const dayOf = (t) => String(t || '').slice(0, 10) || '—'
const pickOwner = (n) => { active.value = active.value === n ? '' : n }
</script>

<template>
  <div class="page">
    <div v-if="err" class="alert-err">{{ err }} <button class="btn btn-sm" @click="load">重试</button></div>

    <!-- 汇总：寄卖是别人的货，这里只看代销规模 -->
    <div class="sum-grid">
      <div class="card sum-card">
        <div class="m-label">寄卖总销售额</div>
        <div class="m-value num">{{ fmtMoney(total.sales) }}</div>
      </div>
      <div class="card sum-card">
        <div class="m-label">寄卖单数</div>
        <div class="m-value num">{{ fmtNum(total.orders) }}<small>单</small></div>
      </div>
      <div class="card sum-card">
        <div class="m-label">应结给寄卖人</div>
        <div class="m-value num">{{ fmtMoney(total.net) }}</div>
      </div>
    </div>

    <!-- 搜索与归属人筛选 -->
    <section class="card consign-tools">
      <div class="search-wrap">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
        <input class="input" v-model="q" placeholder="搜归属人，如 张三" />
      </div>
      <span v-if="active" class="consign-active-chip" @click="active = ''">只看「{{ active }}」 ×</span>
    </section>

    <!-- 按人分账 -->
    <section v-for="o in shown" :key="o.owner" class="card consign-owner" :class="{ open: active === o.owner }">
      <button class="consign-owner-head" @click="pickOwner(o.owner)" :aria-expanded="active === o.owner">
        <span class="consign-avatar" aria-hidden="true">{{ (o.owner || '?')[0] }}</span>
        <span class="consign-owner-main">
          <b>{{ o.owner }}</b>
          <small>{{ o.orders }} 单 · 销售额 {{ fmtMoney(o.sales) }}</small>
        </span>
        <span class="consign-owner-net num">应结 {{ fmtMoney(o.net) }}</span>
        <svg class="consign-chev" :class="{ up: active === o.owner }" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>
      </button>

      <template v-if="active === o.owner">
        <div class="consign-items-head">
          <span>商品</span><span>成交价</span><span>到手</span><span>费用</span><span>时间</span><span>买家</span>
        </div>
        <div v-for="it in o.items" :key="it.id" class="consign-item">
          <span class="cell-ellipsis" :title="it.product">{{ it.product }}</span>
          <b class="num">{{ fmtMoney(it.sell_price) }}</b>
          <b class="num net-col">{{ fmtMoney(it.sell_net) }}</b>
          <span class="muted num">{{ fmtMoney(it.sell_fee + it.sell_shipping) }}</span>
          <span class="muted">{{ dayOf(it.sell_time) }}</span>
          <span class="muted cell-ellipsis">{{ it.counterparty || '—' }}</span>
        </div>
      </template>
      <div v-else-if="o.items.length" class="consign-owner-preview cell-ellipsis">
        {{ o.items.map((i) => i.product).join('、') }}
      </div>
    </section>

    <div v-if="!owners.length && !err" class="empty">
      <div class="empty-badge"><svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.9"/><path d="M16 3.1a4 4 0 0 1 0 7.8"/></svg></div>
      <div class="empty-title">还没有寄卖单</div>
      <div class="empty-sub">在录入或编辑卖单时，把「货物来源」选为「寄卖」并填上归属人（如张三），这里就会按人给你分账。</div>
    </div>
  </div>
</template>

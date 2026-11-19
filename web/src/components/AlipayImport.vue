<script setup>
import { ref, computed } from 'vue'
import { api } from '../api'
import { fmtMoney } from '../utils'
import { maskCloser } from '../maskClose'
import { onMounted, onUnmounted } from 'vue'
// 遮罩点击守卫：在输入框里拖选文字划出弹窗松手时，不会误关弹窗
const mask = maskCloser(() => close)


const emit = defineEmits(['close', 'notify', 'refresh'])

const step = ref('pick')       // pick | review | done
const busy = ref(false)
const fileName = ref('')
const plan = ref(null)
const picked = ref({})
const guideOpen = ref(false)
const result = ref(null)
// 已完结的记录默认不动；勾上这个开关才允许覆盖（已弃单永远不动）
const overrideSettled = ref(false)

async function onFile(e) {
  const f = e.target.files && e.target.files[0]
  if (!f) return
  if (!/\.xlsx$/i.test(f.name)) {
    emit('notify', '请选择支付宝导出的 .xlsx 文件（不是 .csv 或 .xls）', true)
    return
  }
  fileName.value = f.name
  busy.value = true
  try {
    const p = await api.alipayPreview(f)
    plan.value = p
    const sel = {}
    for (const r of (p.rows || [])) {
      if (!r.skip) sel[r.order_no] = true
    }
    picked.value = sel
    step.value = 'review'
  } catch (err) {
    emit('notify', err.message || '解析失败', true)
  } finally {
    busy.value = false
    e.target.value = ''
  }
}

const rows = computed(() => plan.value ? (plan.value.rows || []) : [])
const isSettledSkip = (r) => String(r.skip || '').startsWith('已完结')
const rowPickable = (r) => !r.skip || (overrideSettled.value && isSettledSkip(r))
const pickable = computed(() => rows.value.filter(rowPickable))
const pickedCount = computed(() => Object.keys(picked.value).filter(k => picked.value[k]).length)
const allPicked = computed(() => pickable.value.length > 0 && pickable.value.every(r => picked.value[r.order_no]))
function toggleAll() {
  const on = !allPicked.value
  const m = { ...picked.value }
  for (const r of pickable.value) m[r.order_no] = on
  picked.value = m
}
// 勾上/取消「覆盖已完结」时，把这类行一起选中/取消，省得再逐条点
function toggleOverride() {
  const m = { ...picked.value }
  for (const r of rows.value) {
    if (isSettledSkip(r)) m[r.order_no] = overrideSettled.value
  }
  picked.value = m
}

async function apply() {
  const list = Object.keys(picked.value).filter(k => picked.value[k])
  if (!list.length) { emit('notify', '请先勾选要写入的记录', true); return }
  busy.value = true
  try {
    const r = await api.alipayApply({ plan_id: plan.value.plan_id, order_nos: list, force: overrideSettled.value })
    result.value = r
    step.value = 'done'
    emit('notify', `已更新 ${r.updated} 条记录的费用`)
    emit('refresh')
  } catch (e) {
    emit('notify', e.message || '写入失败', true)
  } finally {
    busy.value = false
  }
}

async function undo() {
  busy.value = true
  try {
    const r = await api.alipayUndo()
    emit('notify', `已撤销，还原了 ${r.restored} 条记录`)
    emit('refresh')
    close()
  } catch (e) {
    emit('notify', e.message || '撤销失败', true)
  } finally {
    busy.value = false
  }
}

function reset() { step.value = 'pick'; plan.value = null; result.value = null; picked.value = {} }
const onEscKey = (e) => { if (e.key === 'Escape') close() }
onMounted(() => document.addEventListener('keydown', onEscKey))
onUnmounted(() => document.removeEventListener('keydown', onEscKey))

function close() { emit('close') }
const kindText = (r) => r.kind === 'fee' ? '手续费' : (r.kind === 'both' ? '手续费+快递' : '快递费')
// 运费被改写时把原值标出来，避免「怎么变了」却看不出来
const shipChanged = (r) => (r.old_ship || 0) > 0.005 && Math.abs((r.old_ship || 0) - (r.shipping || 0)) > 0.005
</script>

<template>
  <Teleport to="body">
    <div class="ali-mask" @pointerdown="mask.onPointerDown" @click="mask.onClick">
      <div class="ali-modal" role="dialog" aria-label="支付宝账单导入">
        <div class="ali-head">
          <div class="ali-head-t">
            <h3>支付宝账单导入</h3>
            <span class="muted">只补费用，不新增订单</span>
          </div>
          <button class="ali-x" aria-label="关闭" @click="close">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><line x1="6" y1="6" x2="18" y2="18"/><line x1="18" y1="6" x2="6" y2="18"/></svg>
          </button>
        </div>

        <!-- 第一步：选文件 -->
        <div v-if="step === 'pick'" class="ali-body">
          <label class="ali-upload" :class="{ busy }">
            <input type="file" accept=".xlsx" hidden @change="onFile" />
            <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="M7 10l5-5 5 5"/><path d="M12 5v12"/></svg>
            <span class="ali-upload-t">
              <b>{{ busy ? '正在解析…' : '选择支付宝交易明细 (.xlsx)' }}</b>
              <span class="muted">手机 / 电脑导出都可以，一次导入覆盖整段时间</span>
            </span>
          </label>

          <button class="ali-guide-toggle" @click="guideOpen = !guideOpen">
            怎么导出？点开看步骤
            <svg class="chev" :class="{ open: guideOpen }" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>
          </button>
          <div v-if="guideOpen" class="ali-guide">
            <div class="ali-guide-block">
              <div class="ali-guide-title">手机支付宝（最快）</div>
              <ol class="ali-steps">
                <li>支付宝 → 右下角「<b>我的</b>」→「<b>账单</b>」</li>
                <li>右上角「<b>…</b>」→「<b>开具交易流水证明</b>」→「<b>用于个人对账</b>」</li>
                <li>时间范围选到覆盖你要核对的那批订单，点「<b>生成</b>」</li>
                <li>填收件邮箱 → 收到后把附件 <code>.xlsx</code> 发到这台设备上传</li>
              </ol>
            </div>
            <div class="ali-guide-block">
              <div class="ali-guide-title">电脑网页版</div>
              <ol class="ali-steps">
                <li>浏览器打开 <code>alipay.com</code> 登录 → 右上角「<b>交易记录</b>」</li>
                <li>选好时间范围，点「<b>导出</b>」→ 下载 Excel</li>
              </ol>
            </div>
            <p class="ali-guide-note muted">
              只需要「全部」交易类型。手续费记录形如 <code>分账-软件服务费(订单号)扣款</code>，自带订单号；
              快递费形如 <code>闲鱼寄件-寄件费_1850739427_LP…</code>，不带订单号，靠<b>发货时间</b>对单（实测只差 2~5 秒）。
            </p>
          </div>
        </div>

        <!-- 第二步：核对 -->
        <div v-else-if="step === 'review'" class="ali-body">
          <div class="ali-sum">
            <span class="ali-chip">手续费命中 <b>{{ plan.fee_hits }}</b></span>
            <span class="ali-chip">快递费 <b>{{ plan.ships }}</b> 笔 · 合计 {{ fmtMoney(plan.ship_sum) }}</span>
            <span class="ali-chip ali-chip-ok">按发货时间精确 <b>{{ plan.exact }}</b></span>
            <span v-if="plan.fee_by_time" class="ali-chip ali-chip-warn">时间匹配 <b>{{ plan.fee_by_time }}</b></span>
            <span v-if="plan.guess" class="ali-chip ali-chip-warn">推测 <b>{{ plan.guess }}</b></span>
            <span v-if="plan.fee_miss" class="ali-chip muted">库里没有 <b>{{ plan.fee_miss }}</b></span>
          </div>
          <label class="ali-force">
            <input type="checkbox" v-model="overrideSettled" @change="toggleOverride" />
            覆盖已完结的记录
            <span class="muted">（默认不动已完结的单；勾上后可以改写它们的手续费与快递费）</span>
          </label>
          <div v-if="plan.warn" class="ali-warn">{{ plan.warn }}</div>

          <div class="ali-table-wrap">
            <table class="ali-table">
              <thead>
                <tr>
                  <th class="c-check"><input type="checkbox" :checked="allPicked" @change="toggleAll" /></th>
                  <th>订单 / 商品</th>
                  <th class="c-fee">服务费</th>
                  <th class="c-fee2">无忧卖</th>
                  <th class="c-ship">快递费</th>
                  <th class="c-net">到手</th>
                  <th class="c-st">状态</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in rows" :key="r.rec_id + '|' + r.kind" :class="{ 'is-skip': !!r.skip && !rowPickable(r) }">
                  <td class="c-check"><input type="checkbox" :disabled="!rowPickable(r)" :checked="!!picked[r.order_no]" @change="picked[r.order_no] = !picked[r.order_no]" /></td>
                  <td class="c-main">
                    <div class="ali-p">{{ r.product || '未命名' }}</div>
                    <div class="ali-sub muted">
                      {{ kindText(r) }}
                      <span v-if="r.fee_guess"> · 按成交时间 {{ (r.fee_rate || 0).toFixed(1) }}% 对上</span>
                      <span v-if="r.ship_time"> · 发货 {{ r.ship_time }}</span>
                      <span v-if="r.ship_gap"> · {{ r.ship_gap }}</span>
                    </div>
                  </td>
                  <td class="c-fee num">{{ fmtMoney(r.fee) }}</td>
                  <td class="c-fee2 num">{{ fmtMoney(r.wuyou) }}</td>
                  <td class="c-ship num">
                    {{ fmtMoney(r.shipping) }}
                    <div v-if="shipChanged(r)" class="ali-old">原 {{ fmtMoney(r.old_ship) }}</div>
                  </td>
                  <td class="c-net num">{{ fmtMoney(r.new_net) }}</td>
                  <td class="c-st">
                    <span v-if="r.skip && isSettledSkip(r) && overrideSettled" class="ali-tag ali-tag-warn">将覆盖</span>
                    <span v-else-if="r.skip" class="ali-tag ali-tag-skip">{{ r.skip }}</span>
                    <span v-else-if="r.fee_guess" class="ali-tag ali-tag-warn">时间匹配 {{ (r.fee_rate || 0).toFixed(1) }}%</span>
                    <span v-else-if="r.ship_exact" class="ali-tag ali-tag-ok">精确匹配</span>
                    <span v-else-if="r.kind !== 'fee'" class="ali-tag ali-tag-warn">推测</span>
                    <span v-else class="ali-tag">订单号匹配</span>
                  </td>
                </tr>
                <tr v-if="!rows.length"><td colspan="7" class="ali-empty muted">账单里没有可用的记录</td></tr>
              </tbody>
            </table>
          </div>

          <div class="ali-foot">
            <button class="btn btn-quiet" @click="reset">重新选择文件</button>
            <span class="muted">已选 {{ pickedCount }} / {{ pickable.length }} 条</span>
            <button class="btn btn-primary" :disabled="busy || !pickedCount" @click="apply">
              {{ busy ? '写入中…' : `写入 ${pickedCount} 条` }}
            </button>
          </div>
        </div>

        <!-- 第三步：完成 -->
        <div v-else class="ali-body">
          <div class="ali-done">
            <div class="ali-done-ico">✓</div>
            <div class="ali-done-t">已更新 <b>{{ result.updated }}</b> 条记录</div>
            <div class="muted">手续费与快递费已按支付宝账单写入；联单关联与完结状态没有改动。</div>
          </div>
          <div class="ali-foot">
            <button class="btn btn-quiet" :disabled="busy" @click="undo">撤销本次导入</button>
            <button class="btn btn-primary" @click="close">完成</button>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>

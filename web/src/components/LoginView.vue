<script setup>
import { ref, onMounted, nextTick } from 'vue'
import { api } from '../api'
import { setShopId, markShopPicked } from '../shopId'

const emit = defineEmits(['success'])

// 两步进入：先验全局密码（一把钥匙管所有店），再选进哪一家。
// 没设全局密码时直接跳到第二步；有店铺密码的店在第二步单独校验。
const stage = ref('global')      // global | shop
const password = ref('')
const loading = ref(false)
const err = ref('')
const shake = ref(false)

const shops = ref([])
const picked = ref(null)
const shopPw = ref('')
const globalEnabled = ref(true)
const globalPassed = ref(false)

function fail(msg) {
  err.value = msg
  shake.value = false
  nextTick(() => { shake.value = true })
}

async function loadShops() {
  try {
    const d = await api.authStatus()
    shops.value = d.shops || []
    globalEnabled.value = !!d.global
    return d
  } catch (e) {
    return {}
  }
}

onMounted(async () => {
  const d = await loadShops()
  // 只有全局会话才算"总钥匙"；店铺会话不享受免店密码的待遇
  if (d.global_session) {
    globalPassed.value = true
    stage.value = 'shop'
  } else if (d.has_session) {
    // 已是某店铺的会话，直接进入（App 通常会跳过登录页，这里兜底）
    emit('success')
    return
  }
  // 没设全局密码：跳过第一步
  if (!d.global) {
    stage.value = 'shop'
  }
})

async function submitGlobal() {
  if (!password.value) { err.value = '请输入访问密码'; return }
  loading.value = true; err.value = ''
  try {
    await api.login(password.value)
    globalPassed.value = true
    stage.value = 'shop'
    password.value = ''
    await loadShops()
  } catch (e) {
    fail(e.message || '密码错误')
    password.value = ''
  } finally {
    loading.value = false
  }
}

function pickShop(s) {
  err.value = ''
  picked.value = s
  shopPw.value = ''
  // 已通过全局密码：总钥匙可进任何店，不必再输店密码
  if (globalPassed.value || !s.has_password) {
    enter(s, '')
  }
}

async function enter(s, pw) {
  loading.value = true; err.value = ''
  try {
    // 全局会话下只切换「当前店铺」，不换掉会话（保留自由切换的能力）；
    // 没有全局会话时（只拿店铺密码），走店铺登录换取绑定该店的会话。
    if (!globalPassed.value) {
      await api.shopLogin(s.id, pw)
    }
    setShopId(s.id)
    markShopPicked()
    emit('success')
  } catch (e) {
    fail(e.message || '密码错误')
    shopPw.value = ''
  } finally {
    loading.value = false
  }
}

function submitShop() {
  if (!picked.value) return
  if (picked.value.has_password && !shopPw.value) { err.value = '请输入该店铺密码'; return }
  enter(picked.value, shopPw.value)
}
</script>

<template>
  <div class="login-wrap">
    <div class="login-aura" aria-hidden="true"></div>
    <div class="login-watermark" aria-hidden="true">鱼</div>
    <div class="login-card" :class="{ shake }">
      <div class="login-mark">鱼</div>
      <h2>闲鱼售卖统计</h2>

      <!-- 第一步：全局密码 -->
      <template v-if="stage === 'global'">
        <p class="muted">请输入访问密码</p>
        <form @submit.prevent="submitGlobal">
          <input class="input" type="password" v-model="password" autofocus
            :class="{ 'is-err': !!err }" :aria-invalid="!!err"
            placeholder="访问密码" autocomplete="current-password" @input="err = ''" />
          <div v-if="err" class="tip err" style="margin-top:10px">{{ err }}</div>
          <button class="btn btn-primary login-btn" :disabled="loading">
            <span v-if="loading" class="spin"></span>下一步
          </button>
        </form>
      </template>

      <!-- 第二步：选择店铺 -->
      <template v-else>
        <p class="muted">选择要进入的店铺</p>
        <div class="login-shops">
          <button v-for="s in shops" :key="s.id" type="button" class="login-shop"
                  :class="{ on: picked && picked.id === s.id }" @click="pickShop(s)">
            <i :style="{ background: s.color }" aria-hidden="true"></i>
            <span>{{ s.name }}</span>
            <svg v-if="s.has_password" class="login-lock" width="13" height="13" viewBox="0 0 24 24"
                 fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <rect x="4" y="10" width="16" height="10" rx="2" /><path d="M8 10V7a4 4 0 0 1 8 0v3" />
            </svg>
          </button>
          <div v-if="!shops.length" class="muted login-empty">暂无可进入的店铺</div>
        </div>

        <!-- 只拿店铺密码进来的人：需要单独校验 -->
        <form v-if="picked && picked.has_password && !globalPassed" @submit.prevent="submitShop">
          <input class="input" type="password" v-model="shopPw" autofocus
            :class="{ 'is-err': !!err }" :aria-invalid="!!err"
            :placeholder="`「${picked.name}」的密码`" autocomplete="current-password" @input="err = ''" />
          <div v-if="err" class="tip err" style="margin-top:10px">{{ err }}</div>
          <button class="btn btn-primary login-btn" :disabled="loading">
            <span v-if="loading" class="spin"></span>进入
          </button>
        </form>

        <div v-if="err && globalPassed" class="tip err" style="margin-top:10px">{{ err }}</div>

        <button v-if="globalEnabled && !globalPassed" type="button" class="login-switch" @click="stage = 'global'; err = ''">
          改用全局密码
        </button>
      </template>

      <div class="muted login-hint">
        忘记密码？可在服务器执行以下命令清除
        <code class="login-code">xianyu-stat --reset-password</code>
      </div>
    </div>
  </div>
</template>

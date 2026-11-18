import { ref, computed } from 'vue'
import { api } from './api'
import { currentShopId, setShopId } from './shopId'

// 店铺列表与当前选择。切换店铺只改 id 并通知上层刷新，
// 不清空任何缓存 —— 各列表组件都靠 refreshKey 重拉，天然按店铺隔离。

export const shops = ref([])
export const activeId = ref(currentShopId())
export const loading = ref(false)
// 只有全局密码会话才能自由切换店铺；用店铺密码登录的账号被锁在那一家，
// 否则界面切到 B、数据仍是 A，看起来就是"串店"。
export const globalSession = ref(true)
export const hasAnyPassword = ref(false)

export const activeShop = computed(
  () => shops.value.find((s) => s.id === activeId.value) || shops.value[0] || null
)

/** 载入店铺列表；若本地记录的 id 已不存在（被删/归档），回落到第一个。 */
export async function loadShops() {
  loading.value = true
  try {
    const data = await api.shops()
    shops.value = data.items || []
    const ids = shops.value.map((s) => s.id)
    if (ids.length && !ids.includes(activeId.value)) {
      switchShop(ids[0])
    }
    if (!ids.length) {
      activeId.value = 0
    }
  } catch (e) {
    // 店铺接口失败不应该让整个应用挂掉，保持默认店铺继续可用
    shops.value = []
  } finally {
    loading.value = false
  }
}

/** 切换店铺：写回本地并更新响应式 id，上层监听 activeId 触发重拉。 */
export function switchShop(id) {
  const n = Number(id) || 0
  if (!n) return
  activeId.value = n
  setShopId(n)
}

export async function createShop(name, color) {
  const r = await api.createShop({ name, color })
  await loadShops()
  if (r && r.id) switchShop(r.id)
  return r
}

export async function renameShop(id, payload) {
  await api.updateShop(id, payload)
  await loadShops()
}

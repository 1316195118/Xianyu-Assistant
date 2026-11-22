import { ref } from 'vue'

// 闲鱼绑定状态的全局镜像：顶栏常驻指示器和「设置 → 闲鱼同步」弹窗共用。
// 只在设置页弹窗里读 status 的话，用户待在明细页就永远看不到失效状态。
export const gfStatus = ref({
  logged_in: false, session_valid: false, order_api_valid: false,
  expired: false, sync_error: '', nick: '', bridge_online: false
})

// 顶栏的「去绑定」请求：Settings 挂载后消费一次并清空
export const gfPanelOpen = ref(false)

// 「去绑定 / 重新绑定」请求：顶栏和失效提示都可以带这个意图进来，
// 同步弹窗挂载后直接进重新绑定表单，不用用户再点一次解绑。
export const gfRebindRequest = ref(false)

import { ref } from 'vue'

// 闲鱼绑定状态的全局镜像：顶栏常驻指示器和「设置 → 闲鱼同步」弹窗共用。
// 只在设置页弹窗里读 status 的话，用户待在明细页就永远看不到失效状态。
export const gfStatus = ref({ logged_in: false, expired: false, sync_error: '', nick: '' })

// 顶栏的「去绑定」请求：Settings 挂载后消费一次并清空
export const gfPanelOpen = ref(false)

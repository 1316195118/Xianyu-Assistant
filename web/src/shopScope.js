import { ref } from 'vue'

// 概览的统计范围：false=当前店铺，true=全部店铺合计。
// 放在顶栏切换，首页不再单独占一行，避免把 Hero 上面的视线打断。
export const allShopsMode = ref(false)

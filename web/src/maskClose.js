/**
 * 遮罩点击关闭的统一守卫。
 *
 * 直接写 `@click.self="close"` 有两个坑：
 *  1. 在输入框里按下鼠标、拖到弹窗外松手时，浏览器会把 click 派发给
 *     「按下与抬起的最近公共祖先」——也就是遮罩本身，`.self` 命中，弹窗被关掉。
 *     用户只是想选中一段文字，结果弹窗没了。
 *  2. 触屏长按拖动同理。
 *
 * 这里改成「必须按下的那一刻就在遮罩上，且抬起也在遮罩上」才关闭。
 * 用法：
 *   const mask = maskCloser(() => emit('close'))
 *   <div class="modal-mask" @pointerdown="mask.onPointerDown" @click="mask.onClick">
 */
export function maskCloser(close) {
  let armed = false
  return {
    onPointerDown(e) {
      armed = e.target === e.currentTarget
    },
    onClick(e) {
      const hit = armed && e.target === e.currentTarget
      armed = false
      if (hit) close()
    }
  }
}

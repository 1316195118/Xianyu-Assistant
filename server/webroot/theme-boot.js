(function () {
  try {
    var theme = localStorage.getItem('xy-theme') || 'system'
    var dark = theme === 'dark' || (theme === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light')
    var motion = localStorage.getItem('xy-motion') || 'system'
    var reduce = motion === 'reduce' || (motion === 'system' && window.matchMedia('(prefers-reduced-motion: reduce)').matches)
    document.documentElement.setAttribute('data-motion', reduce ? 'reduce' : 'full')
  } catch (e) {}

  // 真实可视高度写入 --app-vh：兼容不支持 dvh 的旧内核，软键盘弹出后也按实际高度收缩。
  var setViewportHeight = function () {
    var height = (window.visualViewport && window.visualViewport.height) || window.innerHeight || 0
    if (height > 0) document.documentElement.style.setProperty('--app-vh', height + 'px')
  }
  try {
    setViewportHeight()
    window.addEventListener('resize', setViewportHeight, { passive: true })
    window.addEventListener('orientationchange', setViewportHeight, { passive: true })
    if (window.visualViewport) window.visualViewport.addEventListener('resize', setViewportHeight, { passive: true })
  } catch (e) {}
})()

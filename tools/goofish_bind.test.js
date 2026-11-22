const test = require('node:test')
const assert = require('node:assert/strict')
const { pickLoginPage, authFingerprint, statusNeedsBinding } = require('./goofish_bind')

test('优先选择闲鱼登录页，忽略恢复出来的其他标签页', () => {
  const pages = [
    { type: 'page', url: 'https://chatgpt.com/auth/login' },
    { type: 'page', url: 'https://www.goofish.com/login?from=bind' },
    { type: 'iframe', url: 'https://www.goofish.com/login' }
  ]

  assert.equal(pickLoginPage(pages).url, 'https://www.goofish.com/login?from=bind')
})

test('登录页 URL 变化后回退到任意闲鱼页面', () => {
  const pages = [
    { type: 'page', url: 'https://chatgpt.com/' },
    { type: 'page', url: 'https://www.goofish.com/im' }
  ]

  assert.equal(pickLoginPage(pages).url, 'https://www.goofish.com/im')
})

test('没有闲鱼页面时返回空，不误连其他页面', () => {
  assert.equal(pickLoginPage([{ type: 'page', url: 'https://chatgpt.com/' }]), null)
  assert.equal(pickLoginPage(null), null)
})

test('登录态指纹忽略频繁轮换的签名令牌', () => {
  const a = 'cookie2=stable; unb=u1; _m_h5_tk=old; cna=c1'
  const b = 'cookie2=stable; unb=u1; _m_h5_tk=new; cna=c1'
  assert.equal(authFingerprint(a, 'unb=u1'), authFingerprint(b, 'unb=u1'))
  assert.notEqual(authFingerprint(a, 'unb=u1'), authFingerprint('cookie2=new; unb=u1', 'unb=u1'))
})

test('服务端有效时不重复提交登录态', () => {
  assert.equal(statusNeedsBinding({ logged_in: true, expired: false, sync_error: '' }), false)
  assert.equal(statusNeedsBinding({ logged_in: true, expired: true, sync_error: '' }), true)
  assert.equal(statusNeedsBinding(null), true)
})

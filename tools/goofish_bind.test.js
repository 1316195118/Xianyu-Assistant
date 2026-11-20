const test = require('node:test')
const assert = require('node:assert/strict')
const { pickLoginPage } = require('./goofish_bind')

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

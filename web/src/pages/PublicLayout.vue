<script setup>
import '../public-pages.css'

defineProps({
  eyebrow: { type: String, default: '鱼助手 · 闲鱼售卖统计' },
  title: { type: String, required: true },
  intro: { type: String, default: '' }
})
</script>

<template>
  <div class="public-page">
    <div class="public-aura" aria-hidden="true"></div>
    <header class="public-header">
      <a class="public-brand" href="/">
        <span class="public-mark" aria-hidden="true">鱼</span>
        <span>
          <b>鱼助手</b>
          <small>闲鱼售卖统计</small>
        </span>
      </a>
      <nav class="public-nav" aria-label="公开页面导航">
        <a href="/privacy">隐私政策</a>
        <a href="/terms">服务条款</a>
        <a class="public-back" href="/">返回应用</a>
      </nav>
    </header>

    <main class="public-main">
      <section class="public-hero">
        <p class="public-eyebrow">{{ eyebrow }}</p>
        <h1>{{ title }}</h1>
        <p v-if="intro" class="public-intro">{{ intro }}</p>
        <slot name="hero" />
      </section>
      <slot />
    </main>

    <footer class="public-footer">
      <span>© {{ new Date().getFullYear() }} 鱼助手 · 自托管经营账本</span>
      <span class="public-footer-links">
        <a href="/privacy">隐私政策</a>
        <a href="/terms">服务条款</a>
      </span>
    </footer>
  </div>
</template>

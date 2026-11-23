<script setup>
import PublicLayout from './PublicLayout.vue'
</script>

<template>
  <PublicLayout
    eyebrow="404 · 这条链接走丢了"
    title="没有找到这个页面"
    intro="地址可能输错了，或者旧页面已经移动。你的经营数据不会因为这个错误被修改，可以从下面的入口继续。"
  >
    <div class="public-card">
      <div class="legal-section">
        <h2>试试这些常用入口</h2>
        <div class="public-actions">
          <a class="public-action primary" href="/">返回经营概览</a>
          <a class="public-action" href="/#records">查看交易明细</a>
          <a class="public-action" href="/#groups">打开联单</a>
          <a class="public-action" href="/#settings">检查系统设置</a>
        </div>
      </div>
      <div class="legal-section">
        <h2>如果你是从收藏夹进来的</h2>
        <p>请回到首页重新选择店铺。旧的子路径可能已经改成语义更清晰的 <code>/privacy</code> 或 <code>/terms</code> 页面。</p>
      </div>
    </div>
  </PublicLayout>
</template>

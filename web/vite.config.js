import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import fs from 'node:fs'
import path from 'node:path'

// 改为内容哈希文件名后，旧文件不会被同名覆盖，构建前必须清空 dist/assets，
// 否则 dist 与 server/webroot 会不断堆积历史版本。
function cleanAssets() {
  return {
    name: 'xy-clean-assets',
    apply: 'build',
    buildStart() {
      try {
        fs.rmSync(path.resolve(process.cwd(), 'dist/assets'), { recursive: true, force: true })
      } catch (e) { /* ignore */ }
    }
  }
}

export default defineConfig({
  plugins: [vue(), cleanAssets()],
  // public/ 只放必须在首屏阻塞执行的极小脚本；licenses / OCR WASM 等大批量资源
  // 仍放在项目根 static/，由 build.sh 的 sync_webroot.py 合并，避免重复复制。
  publicDir: 'public',
  build: {
    outDir: 'dist',
    // index.html 保留；assets 由 cleanAssets 在构建前清空，避免旧哈希堆积
    emptyOutDir: false,
    assetsDir: 'assets',
    rollupOptions: {
      output: {
        entryFileNames: 'assets/[name]-[hash].js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash].[ext]'
      }
    }
  }
})

import { svelte } from '@sveltejs/vite-plugin-svelte'
import UnoCSS from 'unocss/vite'
import { defineConfig } from 'vite'

// 产物直接落到 Go 包内：go:embed 只能引用包目录及其子目录，
// 不能是 ../../web/dist。这样两边都不需要拷贝脚本。
export default defineConfig({
  // UnoCSS 放首位：先生成原子类，再交给 svelte 处理组件的 scoped 样式。
  plugins: [UnoCSS(), svelte()],
  build: {
    outDir: '../internal/channel/web/dist',
    emptyOutDir: true,
  },
  server: {
    // 开发时把 API 请求代理到 Go 服务，前端可用热更新
    proxy: {
      '/api': 'http://127.0.0.1:8787',
    },
  },
})

import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    // 前端请求 /api 由 dev server 代理到 Go API, 规避 CORS
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: 'node', // 目前测试只碰纯 TS; 需要 DOM 时再换 jsdom
    include: ['src/**/*.spec.ts'],
  },
})
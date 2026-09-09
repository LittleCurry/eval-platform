import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  test: {
    environment: 'node', // 目前测试只碰纯 TS; 需要 DOM 时再换 jsdom
    include: ['src/**/*.spec.ts'],
  },
})
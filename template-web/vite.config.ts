import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 模板管理台由模板服务器挂载在 /admin 前缀下；构建产物嵌入 Go 二进制。
// 开发模式下 API 请求代理到本机模板服务器（默认 8090）。
export default defineConfig({
  plugins: [react()],
  base: '/admin/',
  build: {
    outDir: '../internal/templateserver/webui/dist',
    emptyOutDir: true,
  },
  server: {
    port: 5174,
    proxy: {
      '/api': 'http://127.0.0.1:8090',
    },
  },
})

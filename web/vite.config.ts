import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath, URL } from 'node:url'

// 前端构建产物被 Go 的 go:embed 内嵌进 apid，并由 apid 在 "/" 下提供。
//
// base 必须是绝对路径 '/'，不能是 './'。
//
// 用相对路径时 index.html 里引用的是 ./assets/index-xxx.js，浏览器按**当前
// 目录**解析。用户直接打开或刷新任何深层路由（/app/rules、/app/settings…）时，
// 它就变成 /app/assets/index-xxx.js —— 而 apid 对不存在的路径会回落到
// index.html，于是浏览器拿到 200 + text/html 当 JS 执行，被 nosniff 挡掉，
// 结果是**白屏且控制台安静**（没有 404、没有报错，最难查的那类）。
//
// 只有单段路径（/、/login）能侥幸正常，所以「首页能打开」完全掩盖了这个问题。
export default defineConfig({
  base: '/',
  plugins: [react()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 1600,
  },
  server: {
    port: 5173,
    host: true,
    proxy: {
      // 本地开发时把 /api 代理到 apid（默认 :8080）
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
  preview: {
    port: 4173,
  },
})

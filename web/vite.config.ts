import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath, URL } from 'node:url'

// 前端构建产物被 Go 的 go:embed 内嵌进 apid，并由 apid 在 "/" 下提供。
// 因此必须使用相对路径 base，保证资源引用不依赖绝对路径前缀。
export default defineConfig({
  base: './',
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

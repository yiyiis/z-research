import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Vite 配置。
// 开发时前端跑在 5173，把 /api 请求代理到后端 8080，免 CORS。
// 生产构建产物输出到 dist/，后续由 Makefile 拷贝到 backend/internal/api/web 供 go:embed。
//
// 经实测：Vite proxy 对 SSE（text/event-stream + chunked）能正常实时透传，
// 无需特殊配置。
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      // WebSocket 研究端点（ws → ws，需 ws:true 启用协议升级）。
      '/ws': {
        target: 'ws://localhost:8080',
        ws: true,
        changeOrigin: true,
      },
      // 普通 REST API。
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})

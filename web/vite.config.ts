import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// 说明：构建产物直接写到 web/dist，会被 Go 的 go:embed 打进二进制。
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // 后端是单二进制，前端体积不敏感；保留 sourcemap 便于线上排查。
    sourcemap: true,
  },
  server: {
    port: 5173,
    // 开发时把接口请求转发到本地后端，前端无需关心跨域。
    proxy: {
      '/api': 'http://127.0.0.1:8099',
      '/healthz': 'http://127.0.0.1:8099',
    },
  },
});

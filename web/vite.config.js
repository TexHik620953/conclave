import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

const target = process.env.CONCLAVE_WEB_TARGET || 'http://127.0.0.1:8787'
const wsTarget = target.replace(/^http/, 'ws')

export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': target,
      '/ws': { target: wsTarget, ws: true },
    },
  },
})

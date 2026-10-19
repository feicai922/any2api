import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  root: fileURLToPath(new URL('.', import.meta.url)),
  build: {
    outDir: '../internal/panel/static',
    emptyOutDir: true,
    assetsDir: 'assets'
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/admin': 'http://127.0.0.1:7862',
      '/api': 'http://127.0.0.1:7862',
      '/console': 'http://127.0.0.1:7862'
    }
  }
})

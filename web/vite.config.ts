import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
export default defineConfig({ plugins: [vue()], server: { proxy: { '/api': 'http://127.0.0.1:18080' } }, build: { outDir: '../internal/webui/dist', emptyOutDir: true, chunkSizeWarningLimit: 1400, rollupOptions: { input: { main: 'index.html', setup: 'setup.html' } } } })

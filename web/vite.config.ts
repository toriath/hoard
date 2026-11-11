import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go server embeds dist/. In development, run the Go server on :8080 and
// `npm run dev`; API calls are proxied (override with HOARD_DEV_BACKEND).
declare const process: { env: Record<string, string | undefined> }

export default defineConfig({
  plugins: [react()],
  // The bundle goes to dist/static, not Vite's default dist/assets: /assets/<id>
  // is a page of the app (deep link to an asset).
  build: { outDir: 'dist', assetsDir: 'static', emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 900 },
  server: {
    proxy: {
      '/api': { target: process.env.HOARD_DEV_BACKEND ?? 'http://127.0.0.1:8080', changeOrigin: false },
    },
  },
})

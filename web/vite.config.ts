import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The build output is embedded into the Go binary via go:embed in
// internal/gui/assets.go. The dev server proxies API and WebSocket
// traffic to a locally running daemon so `npm run dev` works against
// real data (authenticate once via /auth in the same browser).
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/gui/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:19288',
      '/auth': 'http://127.0.0.1:19288',
      '/ws': { target: 'ws://127.0.0.1:19288', ws: true },
    },
  },
})

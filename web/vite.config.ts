import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The build goes into the Go package that embeds it (internal/webui), so the
// daemon and the desktop app serve the same files. `bun run dev` proxies /api to
// a running daemon (MODALFS_UI, the URL `modalfs ui --print` shows), or runs on
// the mock API with VITE_MOCK=1.

// The daemon only answers its own Host header and wants its session token, so
// the dev proxy presents both; the page itself doesn't need the token.
const daemon = process.env.MODALFS_UI ? new URL(process.env.MODALFS_UI) : null
const token = daemon?.hash.match(/token=([0-9a-f]+)/)?.[1] ?? ''

export default defineConfig({
  base: './',
  plugins: [
    react(),
    tailwindcss(),
    // emptyOutDir removes the committed .gitkeep, which lets Go embed the folder before the UI is built.
    { name: 'modalfs-gitkeep', writeBundle: (o) => writeFileSync(join(o.dir!, '.gitkeep'), '') },
  ],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    // One small app; a single chunk loads fastest from the embedded file server.
    chunkSizeWarningLimit: 1200,
  },
  server: {
    proxy: daemon ? { '/api': { target: daemon.origin, changeOrigin: true, headers: { 'X-Modalfs-Token': token } } } : undefined,
  },
})

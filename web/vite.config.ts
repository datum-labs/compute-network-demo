import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// The Go binary embeds the build output, so it lands inside the Go module.
// emptyOutDir stays off to keep the placeholder that lets `go build` work
// before the page has been built; `npm run clean` removes stale output.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: '../internal/site/dist',
    emptyOutDir: false,
    chunkSizeWarningLimit: 900,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
      '/mesh': 'http://localhost:8080',
    },
  },
});

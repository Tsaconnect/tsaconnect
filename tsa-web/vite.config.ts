import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import path from 'node:path';

// Same origin as the API in every environment: in dev, Vite proxies /api to the
// local Go server; in production nginx does. The session cookies depend on it.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': path.resolve(__dirname, 'src') } },
  server: { port: 5174, proxy: { '/api': { target: process.env.API_URL ?? 'http://localhost:5000', changeOrigin: false } } },
  build: {
    rollupOptions: {
      output: {
        // Libraries change far less often than the app: their own chunks stay cached across releases.
        manualChunks(id) {
          if (!id.includes('/node_modules/')) return undefined;
          return /\/node_modules\/(react|react-dom|react-router|react-router-dom|scheduler)\//.test(id) ? 'react' : 'vendor';
        },
      },
    },
  },
});

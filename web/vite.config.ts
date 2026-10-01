import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// ZYNTRA_DEV_API points the dev proxy at a running zyntra serve.
const api = process.env.ZYNTRA_DEV_API || 'http://127.0.0.1:8080';
const upstream = { target: api, secure: false, changeOrigin: true };

export default defineConfig({
  plugins: [react()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    port: 5173,
    proxy: { '/api': upstream, '/healthz': upstream },
  },
});

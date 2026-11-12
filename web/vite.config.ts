import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

const backend = {
  target: process.env.REMOTA_API_TARGET ?? 'http://127.0.0.1:8080',
  changeOrigin: false,
};

export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': backend,
      '/mcp': backend,
      '/healthz': backend,
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.{ts,tsx}', 'e2e/**/*.test.ts'],
    setupFiles: ['src/test/setup.ts'],
  },
});

import path from 'node:path';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.VITE_DAEMON_URL || 'http://127.0.0.1:18080',
        changeOrigin: true,
      },
    },
  },
  build: {
    // The Go binary embeds this directory via //go:embed assets.
    outDir: '../internal/gui/assets',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // Keep stable entry names: internal/gui/server_test.go checks these paths.
        entryFileNames: 'app.js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: (assetInfo) => {
          if (assetInfo.name && assetInfo.name.endsWith('.css')) return 'app.css';
          return 'assets/[name]-[hash][extname]';
        },
      },
    },
  },
});

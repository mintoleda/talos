import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const wsPort = process.env.TALOS_WS_PORT || '8080';

export default defineConfig({
  plugins: [react()],
  root: '.',
  publicDir: 'public',
  server: {
    port: 5173,
    proxy: {
      '/ws': {
        target: `ws://localhost:${wsPort}`,
        ws: true,
      },
    },
  },
});

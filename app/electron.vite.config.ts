import { resolve } from 'path'
import { defineConfig, externalizeDepsPlugin } from 'electron-vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  main: {
    plugins: [externalizeDepsPlugin()],
    build: {
      lib: {
        entry: resolve('main/index.ts'),
      },
    },
  },
  preload: {
    plugins: [externalizeDepsPlugin()],
    build: {
      lib: {
        entry: resolve('main/preload.ts'),
      },
    },
  },
  renderer: {
    root: resolve('renderer'),
    build: {
      rollupOptions: {
        input: {
          index: resolve('renderer/index.html'),
        },
      },
    },
    plugins: [react()],
  },
})

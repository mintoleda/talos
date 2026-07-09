import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    include: ['main/**/*.test.ts', 'renderer/src/**/*.test.ts'],
  },
})

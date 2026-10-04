import { defineConfig } from 'vitest/config';
import vue from '@vitejs/plugin-vue';
import path from 'node:path';

export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { '@': path.resolve(__dirname, './src') } },
  test: {
    environment: 'jsdom',
    globals: true,
    include: [
      'src/**/*.test.ts',
      'src/views/factor/__tests__/factor-contract.spec.ts',
      'tests/**/*.test.ts',
      'tests/cloud-node-workflows.spec.ts',
      'tests/storage-view-browse.spec.ts',
    ],
  },
});

import { defineConfig } from 'vitest/config';
import vue from '@vitejs/plugin-vue';
import path from 'node:path';

// 时间相关的测试要在三个目标时区（UTC、Asia/Shanghai、America/New_York）之外跑：在 UTC 的 CI 和 Docker 里，
// 按 UTC 和按浏览器本地格式化的结果一样，测不出时区泄漏。worker 继承这个环境变量。
process.env.TZ = 'America/Los_Angeles';

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
      'tests/cloud-function-workflows.spec.ts',
      'tests/storage-view-browse.spec.ts',
    ],
  },
});

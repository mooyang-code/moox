import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  globalSetup: process.env.MOOX_REMOTE_PLAYWRIGHT === '1' ? './tests/remote-auth-global-setup.ts' : undefined,
  use: {
    baseURL: process.env.MOOX_REMOTE_BASE_URL || 'http://127.0.0.1:9527',
    trace: process.env.MOOX_REMOTE_PLAYWRIGHT === '1' ? 'off' : 'retain-on-failure',
    video: process.env.MOOX_REMOTE_PLAYWRIGHT === '1' ? 'off' : 'on-first-retry',
    storageState: undefined
  },
  webServer:
    process.env.MOOX_REMOTE_PLAYWRIGHT === '1'
      ? undefined
      : { command: 'pnpm dev --host 127.0.0.1', url: 'http://127.0.0.1:9527', reuseExistingServer: true },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});

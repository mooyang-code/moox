import { defineConfig, devices } from "@playwright/test";

const remote = process.env.MOOX_REMOTE_PLAYWRIGHT === "1";

export default defineConfig({
  testDir: "./tests",
  timeout: 30_000,
  globalSetup: remote ? "./tests/remote-auth-global-setup.ts" : undefined,
  use: {
    baseURL: process.env.MOOX_REMOTE_BASE_URL || "http://127.0.0.1:9527",
    trace: remote ? "off" : "retain-on-failure",
    video: remote ? "off" : "on-first-retry",
    storageState: undefined
  },
  webServer: remote
    ? undefined
    : { command: "pnpm dev --host 127.0.0.1", url: "http://127.0.0.1:9527", reuseExistingServer: true },
  projects: [
    {
      name: "chromium",
      testMatch: remote ? /storage-datanode-management\.remote\.e2e\.spec\.ts/ : undefined,
      use: { ...devices["Desktop Chrome"] }
    }
  ]
});

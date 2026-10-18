import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  timeout: 30_000,
  use: {
    baseURL: "http://127.0.0.1:9527",
    trace: "retain-on-failure",
    video: "on-first-retry",
    storageState: undefined
  },
  webServer: { command: "pnpm dev --host 127.0.0.1", url: "http://127.0.0.1:9527", reuseExistingServer: true },
  projects: [
    {
      name: "chromium",
      testMatch: /\.spec\.ts$/,
      testIgnore: /\.test\.ts$/,
      use: devices["Desktop Chrome"]
    }
  ]
});

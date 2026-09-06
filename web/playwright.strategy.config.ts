import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  testMatch: "strategy-console.spec.ts",
  timeout: 30_000,
  use: {
    baseURL: "http://127.0.0.1:19527",
    ...devices["Desktop Chrome"],
    trace: "retain-on-failure",
    video: "off"
  },
  webServer: {
    command: "pnpm dev --host 127.0.0.1 --port 19527 --strictPort",
    url: "http://127.0.0.1:19527",
    reuseExistingServer: false
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }]
});

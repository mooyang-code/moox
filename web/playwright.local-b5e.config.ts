import { defineConfig, devices } from "@playwright/test";
// 本地临时配置：用系统 Chrome、独立端口，不提交。
export default defineConfig({
  testDir: "./tests",
  testMatch: /zz-shots-b5e\.spec\.ts$/,
  timeout: 60_000,
  outputDir: "/private/tmp/claude-501/-Users-mooyang-Documents-go-src-github-com-mooyang-code-moox/1a05eabb-5c7a-47df-9eb2-82297c083edd/scratchpad/pw-results-b5e",
  use: { baseURL: "http://127.0.0.1:9642", ...devices["Desktop Chrome"], channel: "chrome" },
  webServer: { command: "npx vite --host 127.0.0.1 --port 9642 --strictPort", url: "http://127.0.0.1:9642", reuseExistingServer: false, timeout: 120_000 }
});

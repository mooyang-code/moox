import { defineConfig, devices } from "@playwright/test";

// 浏览器：默认用 Playwright 自带的 Chromium（先 pnpm exec playwright install chromium）；
// 设 PLAYWRIGHT_CHANNEL=chrome 时改用本机安装的 Google Chrome，不用另外下载浏览器；
// 这时用例全过也可能因 Chrome 关闭时唤起 GoogleUpdater 报 worker 退不出、退出码为 1，见 web/README.md。
const channel = process.env.PLAYWRIGHT_CHANNEL || undefined;

export default defineConfig({
  testDir: "./tests",
  testMatch: /\.spec\.ts$/,
  timeout: 30_000,
  // 开发服务器按需编译：每个 worker 的第一次打开页面要现编整棵模块树，机器忙时超过默认的 5 秒，只剩加载图
  expect: { timeout: 15_000 },
  use: {
    baseURL: "http://127.0.0.1:19527",
    trace: "retain-on-failure",
    video: "off"
  },
  // 用单独的端口起开发服务器，不复用本机正在开发用的 9527
  webServer: {
    command: "pnpm dev --host 127.0.0.1 --port 19527 --strictPort",
    url: "http://127.0.0.1:19527",
    reuseExistingServer: false
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], channel } }]
});

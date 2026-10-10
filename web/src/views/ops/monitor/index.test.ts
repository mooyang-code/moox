import fs from "node:fs";
import path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { callControl } from "@/api/admin/http";
import { monitorApi } from "@/api/monitor";

vi.mock("@/api/admin/http", () => ({ callControl: vi.fn() }));

const mockedCallControl = vi.mocked(callControl);
const read = (file: string) => fs.readFileSync(path.resolve(__dirname, file), "utf8");

describe("monitor page", () => {
  beforeEach(() => mockedCallControl.mockReset());

  it("uses only the health overview and global notification APIs", async () => {
    mockedCallControl.mockResolvedValue({ overview: {} });
    await monitorApi.getOverview();
    expect(mockedCallControl).toHaveBeenLastCalledWith("monitor", "GetHealthOverview", {});

    mockedCallControl.mockResolvedValue({ channel: { channel_type: "wecom", configured: false } });
    await monitorApi.getNotification();
    expect(mockedCallControl).toHaveBeenLastCalledWith("monitor", "GetNotificationChannel", {});

    mockedCallControl.mockResolvedValue({ channel: { channel_type: "feishu", configured: true } });
    await monitorApi.updateNotification({ channel_type: "feishu", webhook_url: "https://example.com/hook" });
    expect(mockedCallControl).toHaveBeenLastCalledWith("monitor", "UpdateNotificationChannel", {
      channel_type: "feishu",
      webhook_url: "https://example.com/hook"
    });
  });

  it("shows alerts, the data stages, the component matrix, hosts and notification settings", () => {
    const source = read("index.vue");
    for (const token of [
      "<h2>监控告警</h2>",
      "当前告警",
      "当前没有告警",
      "数据链路",
      "component-matrix",
      "未登记的进程",
      "业务检查",
      "告警推送",
      "推送设置",
      "定位",
      "RawError",
      "createLatestRequestGuard"
    ]) {
      expect(source).toContain(token);
    }
    expect(read("component-drawer.vue")).toContain("探测地址");
    expect(read("notification-modal.vue")).toContain("Modal.warning");
  });

  it("shows the backend summary instead of translating reasons in the browser", () => {
    const sources = ["index.vue", "component-drawer.vue", "monitor-display.ts"].map(read).join("\n");
    expect(sources).not.toContain("displayConclusion");
    expect(sources).not.toContain("health check failed");
    expect(sources).not.toContain("reporter fresh");
  });
});

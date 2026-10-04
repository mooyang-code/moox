import fs from "node:fs";
import path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { callControl } from "@/api/admin/http";
import { healthMonitorApi } from "@/api/health-monitor";
import { displayConclusion } from "./health-display";

vi.mock("@/api/admin/http", () => ({ callControl: vi.fn() }));

const mockedCallControl = vi.mocked(callControl);

describe("health monitor page", () => {
  beforeEach(() => mockedCallControl.mockReset());

  it("keeps the page user-facing and guards refresh requests", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    expect(source).toContain("createLatestRequestGuard");
    expect(source).toContain("Modal.warning");
    expect(source).toContain("系统一切正常");
    expect(source).toContain("clear-state");
    expect(source).toContain("systemHealthy");
    for (const token of [["新增", "探测"].join(""), ["手动", "运行"].join(""), ["原始", "指标名"].join(""), ["Headers", " JSON"].join("")]) {
      expect(source).not.toContain(token);
    }
  });

  it("uses only the health overview and global notification APIs", async () => {
    mockedCallControl.mockResolvedValue({ overview: { alerts: [], business_items: [], service_items: [] } });
    await healthMonitorApi.getOverview();
    expect(mockedCallControl).toHaveBeenLastCalledWith("moox_monitor", "GetHealthOverview", {});

    mockedCallControl.mockResolvedValue({ channel: { channel_type: "wecom", configured: false } });
    await healthMonitorApi.getNotification();
    expect(mockedCallControl).toHaveBeenLastCalledWith("moox_monitor", "GetNotificationChannel", {});
  });

  it("renders monitoring conclusions in Chinese", () => {
    expect(displayConclusion("health check failed")).toBe("健康检查失败");
    expect(displayConclusion("reporter fresh")).toBe("监控上报正常");
    expect(displayConclusion("unexpected timeout")).toBe("监控检查失败，请查看日志详情");
    expect(displayConclusion("已经完成检查")).toBe("已经完成检查");
  });
});

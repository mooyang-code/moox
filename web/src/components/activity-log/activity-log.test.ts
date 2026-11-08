import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ listActivityLogs: vi.fn() }));
vi.mock("@/api/admin/activity", () => api);
import ActivityLog from "./index.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

describe("操作记录", () => {
  it("读取失败时显示原因和重试，重试成功后清掉", async () => {
    api.listActivityLogs.mockRejectedValueOnce(new Error("Admin 不可用"));
    const wrapper = mount(ActivityLog, { props: { spaceId: "crypto" }, global: { plugins: [ArcoVue] } });
    await flushPromises();
    expect(wrapper.text()).toContain("操作记录读取失败：Admin 不可用");
    api.listActivityLogs.mockResolvedValueOnce({ items: [], page: { total: 0 } });
    await wrapper
      .findAll("button")
      .find(item => item.text() === "重试")!
      .trigger("click");
    await flushPromises();
    expect(wrapper.text()).not.toContain("操作记录读取失败");
  });
});

describe("级别与类别的显示", () => {
  it("级别写中文，普通信息不用绿色；类别写中文名，没有登记的原样显示", async () => {
    const { activityCategoryLabel, activityLevelColor, activityLevelText } = await import("./activity-log");
    expect([activityLevelText("error"), activityLevelText("warning"), activityLevelText("info")]).toEqual([
      "错误",
      "警告",
      "信息"
    ]);
    expect(activityLevelColor("info")).not.toContain("green");
    expect(activityCategoryLabel("collector.subject_change")).toBe("标的变更");
    expect(activityCategoryLabel("trade.custom")).toBe("trade.custom");
  });
});

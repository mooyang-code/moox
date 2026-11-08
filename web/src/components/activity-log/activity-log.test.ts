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

describe("操作记录的筛选", () => {
  it("选类别、来源回车、选时间范围各只发一次请求，回到第 1 页", async () => {
    api.listActivityLogs.mockReset().mockResolvedValue({ items: [], page: { total: 0 } });
    const wrapper = mount(ActivityLog, { props: { spaceId: "crypto", showFilters: true }, global: { plugins: [ArcoVue] } });
    await flushPromises();
    const base = api.listActivityLogs.mock.calls.length;

    const select = wrapper.findComponent({ name: "Select" });
    select.vm.$emit("update:modelValue", "collector.subject_change");
    select.vm.$emit("change", "collector.subject_change");
    await flushPromises();
    expect(api.listActivityLogs.mock.calls.length).toBe(base + 1);
    expect(JSON.stringify(api.listActivityLogs.mock.lastCall)).toContain("collector.subject_change");

    const input = wrapper.find("input[placeholder*='来源模块']");
    await input.setValue("collector");
    await input.trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(api.listActivityLogs.mock.calls.length).toBe(base + 2);

    wrapper
      .findComponent({ name: "ZonedRangePicker" })
      .vm.$emit("update:modelValue", [Date.UTC(2026, 9, 1), Date.UTC(2026, 9, 2)]);
    await flushPromises();
    expect(api.listActivityLogs.mock.calls.length).toBe(base + 3);
    wrapper.unmount();
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

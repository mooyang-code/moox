import { mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { describe, expect, it, vi } from "vitest";
import ZonedDatePicker from "./zoned-date-picker.vue";
import ZonedRangePicker from "./zoned-range-picker.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const mountPicker = (component: unknown, props: Record<string, unknown>) =>
  mount(component as never, { props, global: { plugins: [ArcoVue] } });

describe("按时区选时间", () => {
  it("选到的墙钟按指定时区换成时刻，和浏览器本地时区无关，旁边写后缀", () => {
    const wrapper = mountPicker(ZonedDatePicker, { zone: "Asia/Shanghai", modelValue: null });
    (wrapper.vm as unknown as { onChange(value: string): void }).onChange("2026-11-07 09:30:00");
    expect(wrapper.emitted("update:modelValue")?.[0]).toEqual([Date.parse("2026-11-07T01:30:00Z")]);
    expect(wrapper.text()).toContain("UTC+8");
  });

  it("时刻按指定时区显示在选择器里", () => {
    const wrapper = mountPicker(ZonedDatePicker, { zone: "UTC", modelValue: Date.parse("2026-11-07T01:30:00Z") });
    expect(wrapper.find("input").element.value).toBe("2026-11-07 01:30");
    expect(wrapper.text()).toContain("UTC");
  });

  it("清空时给出 null", () => {
    const wrapper = mountPicker(ZonedDatePicker, { zone: "UTC", modelValue: 1 });
    (wrapper.vm as unknown as { onChange(value: unknown): void }).onChange(undefined);
    expect(wrapper.emitted("update:modelValue")?.[0]).toEqual([null]);
  });
});

describe("按时区选范围", () => {
  it("带时间时起止按指定时区换成时刻", () => {
    const wrapper = mountPicker(ZonedRangePicker, { zone: "Asia/Shanghai", modelValue: null });
    (wrapper.vm as unknown as { onChange(value: string[]): void }).onChange(["2026-11-07 09:30:00", "2026-11-07 15:00:00"]);
    expect(wrapper.emitted("update:modelValue")?.[0]).toEqual([
      [Date.parse("2026-11-07T01:30:00Z"), Date.parse("2026-11-07T07:00:00Z")]
    ]);
  });

  it("只选日期时结束日期整天都算在内：止是下一天零点", () => {
    const wrapper = mountPicker(ZonedRangePicker, { zone: "Asia/Shanghai", modelValue: null, showTime: false });
    (wrapper.vm as unknown as { onChange(value: string[]): void }).onChange(["2026-11-01", "2026-11-07"]);
    expect(wrapper.emitted("update:modelValue")?.[0]).toEqual([
      [Date.parse("2026-10-31T16:00:00Z"), Date.parse("2026-11-07T16:00:00Z")]
    ]);
  });
});

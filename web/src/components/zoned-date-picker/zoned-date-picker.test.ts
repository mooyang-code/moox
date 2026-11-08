import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import { msToWallClock } from "@/utils/time-zone";
import ZonedDatePicker from "./zoned-date-picker.vue";
import ZonedRangePicker from "./zoned-range-picker.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

let wrapper: VueWrapper;
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

const mountPicker = (component: unknown, props: Record<string, unknown>, attrs: Record<string, unknown> = {}) => {
  wrapper = mount(component as never, { props: { ...props, ...attrs }, global: { plugins: [ArcoVue] }, attachTo: document.body });
  return wrapper;
};

async function openPanel() {
  await wrapper.find(".arco-picker").trigger("click");
  await flushPromises();
}

/** 面板里当前月份的日期格子 */
const dayCells = () => [...document.querySelectorAll<HTMLElement>(".arco-picker-cell-in-view .arco-picker-date")];
/** 弹出面板里的按钮（确定、此刻、快捷范围都是 button） */
const buttonWithText = (text: string) =>
  [...document.querySelectorAll<HTMLElement>("button")].find(item => item.textContent?.trim() === text);
const lastEmitted = () => wrapper.emitted("update:modelValue")?.at(-1)?.[0];

describe("按时区选时间：真实操作", () => {
  it("没有值时点日期格子再点确定：时间是 00:00，不带浏览器本地现在的秒数，按所给时区换成时刻", async () => {
    mountPicker(ZonedDatePicker, { zone: "Asia/Shanghai", modelValue: null });
    await openPanel();
    dayCells()[9].click();
    await flushPromises();
    buttonWithText("确定")?.click();
    await flushPromises();
    const ms = lastEmitted() as number;
    expect(typeof ms).toBe("number");
    expect(msToWallClock(ms, "Asia/Shanghai")).toMatch(/ 00:00:00$/);
  });

  it("“此刻”按所给时区取当前时刻（到分钟），点了就提交", async () => {
    mountPicker(ZonedDatePicker, { zone: "Asia/Shanghai", modelValue: null });
    await openPanel();
    const before = Math.floor(Date.now() / 60000) * 60000;
    buttonWithText("此刻")?.click();
    await flushPromises();
    const ms = lastEmitted() as number;
    expect(ms === before || ms === before + 60000).toBe(true);
  });

  it("时刻按所给时区显示在输入框里，旁边写后缀", () => {
    mountPicker(ZonedDatePicker, { zone: "UTC", modelValue: Date.parse("2026-11-07T01:30:00Z") });
    expect(wrapper.find("input").element.value).toBe("2026-11-07 01:30");
    expect(wrapper.text()).toContain("UTC");
  });

  it("class 和 style 落在外层容器上", () => {
    mountPicker(ZonedDatePicker, { zone: "UTC" }, { class: "outer", style: "width: 100%" });
    expect(wrapper.classes()).toContain("outer");
    expect(wrapper.attributes("style")).toContain("width: 100%");
  });
});

describe("按时区选范围：真实操作", () => {
  it("快捷范围按毫秒给出，提交的就是这两个时刻，不会差一个时区偏移", async () => {
    const end = Date.parse("2026-11-07T01:16:00Z");
    mountPicker(ZonedRangePicker, {
      zone: "UTC",
      modelValue: null,
      quickRanges: [{ label: "最近 1 小时", range: () => [end - 3_600_000, end] }]
    });
    await openPanel();
    buttonWithText("最近 1 小时")?.click();
    await flushPromises();
    expect(lastEmitted()).toEqual([end - 3_600_000, end]);
  });

  it("只选日期时结束日期整天都算在内：止是下一天零点；显示时退回前一天", () => {
    const range: [number, number] = [Date.parse("2026-10-31T16:00:00Z"), Date.parse("2026-11-07T16:00:00Z")];
    mountPicker(ZonedRangePicker, { zone: "Asia/Shanghai", modelValue: range, showTime: false });
    const inputs = wrapper.findAll("input").map(item => (item.element as HTMLInputElement).value);
    expect(inputs).toEqual(["2026-11-01", "2026-11-07"]);
  });
});

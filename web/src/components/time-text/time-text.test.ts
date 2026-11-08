import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import TimeText from "./index.vue";

describe("带后缀的时间文本", () => {
  it("按给定精度显示，悬停是到秒的完整时间", () => {
    const wrapper = mount(TimeText, { props: { value: "2026-11-07T01:30:05Z", zone: "Asia/Shanghai", precision: "minute" } });
    expect(wrapper.text()).toBe("2026-11-07 09:30 UTC+8");
    expect(wrapper.attributes("title")).toBe("2026-11-07 09:30:05 UTC+8");
  });

  it("默认到秒、UTC；没有时间时为 —", () => {
    expect(mount(TimeText, { props: { value: "2026-11-07T01:30:05Z" } }).text()).toBe("2026-11-07 01:30:05 UTC");
    expect(mount(TimeText, { props: { value: null } }).text()).toBe("—");
  });
});

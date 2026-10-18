import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import TargetTable from "./strategy-target-table.vue";

const stubs = {
  "a-tag": { template: '<span class="tag"><slot /></span>' },
  "a-alert": { template: '<div class="alert"><slot /></div>' }
};
const snapshot = {
  targets: [
    { instrument_id: "ETH-USDT", target_weight: "0.1" },
    { instrument_id: "BTC-USDT", target_weight: "0.4" },
    { instrument_id: "SOL-USDT", target_weight: "-0.2" }
  ],
  session_id: "s",
  bar_end_time: "2026-10-10T11:00:00Z",
  valid_until: "2026-10-10T12:00:00Z",
  result_id: "r"
};

function render(state: string) {
  return mount(TargetTable, { props: { targets: snapshot.targets, snapshot, state } as never, global: { stubs } });
}

describe("strategy target table", () => {
  it("lists targets by absolute weight with long/short sides", () => {
    const wrapper = render("valid");
    const ids = wrapper.findAll(".target-id").map(item => item.text());
    expect(ids).toEqual(["BTC-USDT", "SOL-USDT", "ETH-USDT"]);
    expect(wrapper.text()).toContain("-20.00%");
    expect(wrapper.find(".tag").text()).toBe("目标有效");
    expect(wrapper.find(".alert").exists()).toBe(false);
  });

  it("still shows the last targets when they expired, dimmed and with a notice", () => {
    const wrapper = render("expired");
    expect(wrapper.findAll(".target-id")).toHaveLength(3);
    expect(wrapper.find(".target-section").classes()).toContain("is-stale");
    expect(wrapper.find(".alert").text()).toContain("目标已经过期");
  });

  it("still shows the last targets of a disabled instance", () => {
    const wrapper = render("inactive");
    expect(wrapper.findAll(".target-id")).toHaveLength(3);
    expect(wrapper.find(".alert").text()).toContain("实例已停用");
  });

  it("shows a reading state instead of a verdict before the first read finishes", () => {
    const wrapper = mount(TargetTable, {
      props: { targets: [], snapshot: null, state: "unknown", loading: true } as never,
      global: { stubs }
    });
    expect(wrapper.text()).toContain("正在读取当前目标");
    expect(wrapper.text()).not.toContain("无法确认");
    expect(wrapper.find(".alert").exists()).toBe(false);
  });

  it("labels the bar column as distribution and the number column as target weight", () => {
    const heads = render("valid")
      .findAll('[role="columnheader"]')
      .map(item => item.text());
    expect(heads).toEqual(["标的", "权重分布", "目标权重", "方向"]);
  });
});

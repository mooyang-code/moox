import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import TargetTable from "./strategy-target-table.vue";

const stubs = {
  "a-tag": { template: '<span class="tag"><slot /></span>' },
  "a-alert": { template: '<div class="alert"><slot /></div>' },
  "a-input": {
    props: ["modelValue", "size", "allowClear", "placeholder"],
    emits: ["update:modelValue"],
    template: '<input class="search" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
  },
  "a-pagination": {
    props: ["current", "pageSize", "total", "size", "pageSizeOptions", "showPageSize"],
    emits: ["update:current", "update:pageSize"],
    template: '<div class="pager" :data-current="current" :data-page-size="pageSize" :data-total="total" />'
  }
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

  it("summarizes sides, gross weight and the weight range", () => {
    const text = render("valid").find(".target-summary").text();
    expect(text).toContain("多 / 空2 / 1");
    expect(text).toContain("权重合计70.0%");
    expect(text).toContain("最大 / 最小40.00% / 10.00%");
  });

  it("keeps a short list in one column without search or paging", () => {
    const wrapper = render("valid");
    expect(wrapper.findAll(".target-col")).toHaveLength(1);
    expect(wrapper.find(".search").exists()).toBe(false);
    expect(wrapper.find(".pager").exists()).toBe(false);
  });
});

// 45 个标的：权重 T01 最大、依次递减，方便核对排序与分页。
function manyTargets(count: number) {
  return Array.from({ length: count }, (_, index) => ({
    instrument_id: `T${String(index + 1).padStart(2, "0")}`,
    target_weight: String((count - index) / 1000)
  }));
}
function renderMany(count: number) {
  return mount(TargetTable, { props: { targets: manyTargets(count), snapshot, state: "valid" } as never, global: { stubs } });
}
const ids = (wrapper: ReturnType<typeof renderMany>) => wrapper.findAll(".target-id").map(item => item.text());

describe("strategy target table with many targets", () => {
  it("shows 20 per page in two columns, filling the left column first", () => {
    const wrapper = renderMany(45);
    expect(ids(wrapper)).toEqual(Array.from({ length: 20 }, (_, index) => `T${String(index + 1).padStart(2, "0")}`));
    const columns = wrapper.findAll(".target-col");
    expect(columns).toHaveLength(2);
    expect(columns[0].findAll(".target-id")).toHaveLength(10);
    expect(columns[1].findAll(".target-id")[0].text()).toBe("T11");
    expect(wrapper.findAll('[role="columnheader"]')).toHaveLength(8);
    expect(wrapper.find(".target-range").text()).toBe("第 1–20 条，共 45 条");
    expect(wrapper.find(".pager").attributes("data-total")).toBe("45");
  });

  it("moves to the requested page", async () => {
    const wrapper = renderMany(45);
    await wrapper.findComponent(".pager").vm.$emit("update:current", 2);
    expect(ids(wrapper)[0]).toBe("T21");
    expect(wrapper.find(".target-range").text()).toBe("第 21–40 条，共 45 条");
  });

  it("filters by instrument id, resets to the first page and hides the pager when the result fits", async () => {
    const wrapper = renderMany(45);
    await wrapper.findComponent(".pager").vm.$emit("update:current", 3);
    await wrapper.find(".search").setValue("t0");
    expect(ids(wrapper)).toEqual(Array.from({ length: 9 }, (_, index) => `T0${index + 1}`));
    expect(wrapper.findAll(".target-col")).toHaveLength(1);
    expect(wrapper.find(".target-summary").text()).toContain("匹配9");
    expect(wrapper.find(".pager").exists()).toBe(false);
    await wrapper.find(".search").setValue("zzz");
    expect(wrapper.text()).toContain("没有匹配的标的");
  });

  it("keeps the page within range when a refresh leaves fewer targets", async () => {
    const wrapper = renderMany(45);
    await wrapper.findComponent(".pager").vm.$emit("update:current", 3);
    expect(ids(wrapper)[0]).toBe("T41");
    await wrapper.setProps({ targets: manyTargets(25) } as never);
    expect(ids(wrapper)[0]).toBe("T21");
    expect(wrapper.find(".target-range").text()).toBe("第 21–25 条，共 25 条");
    await wrapper.setProps({ targets: manyTargets(8) } as never);
    expect(ids(wrapper)[0]).toBe("T01");
  });
});

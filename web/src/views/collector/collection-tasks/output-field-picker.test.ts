import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, describe, expect, it } from "vitest";
import type { DataTypeConfig } from "@/api/collector";
import { baseOutputFieldIds, buildOutputFieldCatalog } from "./collection-task-params";
import OutputFieldPicker from "./output-field-picker.vue";

// 模拟 GetDataTypeConfigs 的返回：显示名、分组只来自 Collector 的字段规格。
const detail: DataTypeConfig = {
  data_type: "kline",
  type_name: "K 线",
  base_output_fields: [
    { field_id: "open", display_name: "开盘价", group: "行情价格", value_type: "double" },
    { field_id: "close", display_name: "收盘价", group: "行情价格", value_type: "double" },
    { field_id: "volume", display_name: "成交量", group: "成交估值", value_type: "double" },
    { field_id: "amount", display_name: "成交额", group: "成交估值", value_type: "double" }
  ],
  extra_fields: [
    {
      field_id: "turnover_rate",
      display_name: "换手率",
      group: "成交估值",
      market_id: "stockcn",
      instrument_type: "equity",
      frequencies: ["1d"],
      provider_id: "eastmoney"
    },
    {
      field_id: "sw_l1",
      display_name: "申万一级行业",
      group: "行业",
      market_id: "stockcn",
      instrument_type: "equity",
      frequencies: ["1d"],
      provider_id: "swsresearch"
    }
  ]
};

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

async function mountPicker(available: string[]) {
  wrapper = mount(OutputFieldPicker, {
    props: {
      visible: true,
      selected: [],
      catalog: buildOutputFieldCatalog(detail.base_output_fields, detail.extra_fields),
      available: new Set(available),
      base: baseOutputFieldIds(detail.base_output_fields)
    },
    global: { plugins: [ArcoVue] },
    attachTo: document.body
  });
  await flushPromises();
}

function sections() {
  return [...document.querySelectorAll(".output-field-section")].map(section => ({
    name: section.querySelector(".output-field-section__header strong")?.textContent?.trim(),
    fields: [...section.querySelectorAll(".output-field-option__text strong")].map(item => item.textContent?.trim())
  }));
}

describe("输出字段选择器", () => {
  it("按字段规格的分组显示中文名，附加字段只在可选时出现", async () => {
    await mountPicker(["turnover_rate"]);
    expect(sections()).toEqual([
      { name: "行情价格", fields: ["开盘价", "收盘价"] },
      { name: "成交估值", fields: ["成交量", "成交额", "换手率"] }
    ]);
  });

  it("勾选后点“确认选择”才写回已选字段", async () => {
    await mountPicker([]);
    const checkbox = document.querySelector(".output-field-option input") as HTMLInputElement;
    checkbox.click();
    await flushPromises();
    expect(wrapper?.emitted("update:selected")).toBeUndefined();
    const confirm = [...document.querySelectorAll(".arco-drawer-footer button")].find(item =>
      item.textContent?.includes("确认选择")
    );
    (confirm as HTMLElement).click();
    await flushPromises();
    expect(wrapper?.emitted("update:selected")?.[0]).toEqual([["open"]]);
  });
});

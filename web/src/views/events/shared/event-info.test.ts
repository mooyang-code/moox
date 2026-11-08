import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { describe, expect, it } from "vitest";
import type { SubscribableEvent } from "@/api/callback-dispatcher";
import EventInfo from "./event-info.vue";

const base: SubscribableEvent = {
  event_name: "event.collector.instrument.delist_scheduled",
  version: 1,
  title: "标的即将下架",
  description: "交易所公布了某个标的的下架时间",
  scope_fields: [],
  sample_json: "",
  payload_fields: []
};

describe("事件说明", () => {
  it("有示例时列出 data 字段，并把示例格式化后显示", async () => {
    const wrapper = mount(EventInfo, {
      props: {
        event: {
          ...base,
          sample_json: '{"event":"event.collector.instrument.delist_scheduled","data":{"exchange":"binance"}}',
          payload_fields: [{ field: "exchange", title: "交易所：binance 或 okx" }]
        }
      },
      global: { plugins: [ArcoVue] }
    });
    await flushPromises();
    expect(wrapper.text()).toContain("交易所：binance 或 okx");
    expect(wrapper.find("pre").text()).toContain('  "data": {\n    "exchange": "binance"');
  });

  it("旧版本没返回示例和字段说明时只显示说明", () => {
    const wrapper = mount(EventInfo, { props: { event: base }, global: { plugins: [ArcoVue] } });
    expect(wrapper.text()).toContain("交易所公布了某个标的的下架时间");
    expect(wrapper.find("table").exists()).toBe(false);
    expect(wrapper.find("pre").exists()).toBe(false);
  });
});

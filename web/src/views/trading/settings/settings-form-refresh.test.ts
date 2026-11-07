import { mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { describe, expect, it, vi } from "vitest";
import FundSettingsForm from "./fund-settings-form.vue";
import OrderSettingsForm from "./order-settings-form.vue";
import { defaultFundSettings } from "./fund-settings-form";
import { defaultOrderSettings } from "./order-settings-form";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

function inputOf(wrapper: ReturnType<typeof mount>, label: string): HTMLInputElement {
  const item = wrapper.findAll(".arco-form-item").find(node => node.find(".arco-form-item-label").text() === label);
  expect(item, label).toBeTruthy();
  return item!.find("input").element as HTMLInputElement;
}

// 详情页每 5 秒整体换一次组合对象：内容没变的新对象不能把正在改的输入还原；内容真的变了（保存后重读）才重置。
describe("设置表单被轮询刷新时", () => {
  it("下单设置：内容相同的新对象不冲掉未保存的修改，内容变了才重置", async () => {
    const wrapper = mount(OrderSettingsForm, { props: { settings: defaultOrderSettings() }, global: { plugins: [ArcoVue] } });
    const input = inputOf(wrapper, "现货最小下单额");
    const original = input.value;
    await wrapper
      .findAll(".arco-form-item")
      .find(node => node.text().includes("现货最小下单额"))!
      .find("input")
      .setValue("20");
    expect(input.value).toBe("20");

    await wrapper.setProps({ settings: { ...defaultOrderSettings() } });
    expect(input.value, "内容相同的新对象").toBe("20");

    await wrapper.setProps({ settings: { ...defaultOrderSettings(), min_notional_spot: "33" } });
    expect(input.value, "已保存的内容变了").toBe("33");
    expect(original).not.toBe("33");
  });

  it("资金设置：同样不冲掉未保存的修改", async () => {
    const wrapper = mount(FundSettingsForm, { props: { settings: defaultFundSettings() }, global: { plugins: [ArcoVue] } });
    const input = inputOf(wrapper, "资金预留比例");
    await wrapper
      .findAll(".arco-form-item")
      .find(node => node.text().includes("资金预留比例"))!
      .find("input")
      .setValue("0.25");
    await wrapper.setProps({ settings: { ...defaultFundSettings() } });
    expect(input.value).toBe("0.25");
    await wrapper.setProps({ settings: { ...defaultFundSettings(), reserve_ratio: "0.4" } });
    expect(input.value).toBe("0.4");
  });
});

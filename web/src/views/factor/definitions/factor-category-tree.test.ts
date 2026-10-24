import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import FactorCategoryTree from "./factor-category-tree.vue";

const categories = [
  { category_id: "trend", name: "趋势类", sort_order: 1 },
  { category_id: "uncategorized", name: "未分类" }
];

describe("因子分类树", () => {
  it("显示全部与各分类数量；未分类没有重命名、删除入口", async () => {
    const wrapper = mount(FactorCategoryTree, {
      props: { categories, counts: { trend: 3 }, total: 5, selected: "trend" },
      global: {
        stubs: {
          "a-dropdown": { template: '<div class="menu"><slot /><slot name="content" /></div>' },
          "a-doption": { emits: ["click"], template: '<span class="option" @click="$emit(\'click\')"><slot /></span>' },
          "a-button": { template: "<button><slot /></button>" },
          "a-tooltip": { template: "<div><slot /></div>" },
          "a-modal": true,
          "a-form": true,
          "a-form-item": true,
          "a-input": true
        }
      }
    });
    const items = wrapper.findAll("button.tree-item").map(item => item.text());
    expect(items).toEqual(["全部因子5", "趋势类3", "未分类0"]);
    expect(wrapper.find("button.tree-item.active").text()).toBe("趋势类3");
    expect(wrapper.findAll(".menu")).toHaveLength(1);

    await wrapper
      .findAll(".option")
      .find(option => option.text() === "删除")!
      .trigger("click");
    expect(wrapper.emitted("delete")?.[0]).toEqual([categories[0]]);
    await wrapper.findAll("button.tree-item")[2].trigger("click");
    expect(wrapper.emitted("select")?.[0]).toEqual(["uncategorized"]);
  });
});

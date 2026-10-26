import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h, inject, provide, ref, toRef, type Ref } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FactorInfo } from "@/api/factor/types";

const mocks = vi.hoisted(() => ({
  listFactors: vi.fn(),
  listFactorCategories: vi.fn(),
  setFactorCategory: vi.fn(),
  deleteFactorCategory: vi.fn(),
  upsertFactorCategory: vi.fn(),
  push: vi.fn(),
  replace: vi.fn(),
  route: undefined as { path: string; query: Record<string, string> } | undefined,
  spaceId: undefined as { value: string } | undefined
}));

vi.mock("@/api/factor", () => ({
  listFactors: mocks.listFactors,
  listFactorCategories: mocks.listFactorCategories,
  setFactorCategory: mocks.setFactorCategory,
  deleteFactorCategory: mocks.deleteFactorCategory,
  upsertFactorCategory: mocks.upsertFactorCategory,
  deleteFactor: vi.fn()
}));
vi.mock("@/views/factor/shared/use-factor-scope", async () => {
  const { ref: vueRef } = await import("vue");
  mocks.spaceId = vueRef("");
  return {
    useFactorScope: () => ({
      store: { tasks: [], reload: vi.fn(), taskLabel: (set: { task_id: string }) => set.task_id },
      spaceId: mocks.spaceId
    })
  };
});
vi.mock("vue-router", async () => {
  const { reactive: vueReactive } = await import("vue");
  mocks.route = vueReactive({ path: "/factor/definitions", query: {} as Record<string, string> });
  return { useRouter: () => ({ push: mocks.push, replace: mocks.replace }), useRoute: () => mocks.route };
});
vi.mock("@arco-design/web-vue", () => ({
  Message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Modal: { confirm: (options: { onOk: () => unknown }) => options.onOk() }
}));

import DefinitionsPage from "./index.vue";

const info = (id: string, category?: string): FactorInfo => ({
  factor: {
    factor_id: id,
    name: `${id}_module`,
    factor_type: "timeseries",
    spec_hash: "",
    input_columns: ["close"],
    outputs: [id.toLowerCase()],
    lookback_periods: 1,
    allow_partial_universe: false,
    category_id: category,
    created_at: "",
    updated_at: ""
  },
  usages: []
});

// 表格桩：每行渲染全部列的 cell 插槽，点行触发 row-click。
const RowProvider = defineComponent({
  props: { record: { type: Object, required: true } },
  setup(props, { slots }) {
    provide("record", toRef(props, "record"));
    return () => slots.default?.();
  }
});
const TableStub = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  emits: ["row-click"],
  setup(props, { slots, emit }) {
    return () =>
      h(
        "div",
        (props.data as FactorInfo[]).map(record =>
          h("div", { class: "row", onClick: () => emit("row-click", record) }, [
            h(RowProvider, { record }, () => slots.columns?.())
          ])
        )
      );
  }
});
const ColumnStub = defineComponent({
  setup(_, { slots }) {
    const record = inject<Ref<unknown>>("record", ref());
    return () => h("div", slots.cell?.({ record: record.value }));
  }
});
const TreeStub = defineComponent({
  name: "FactorCategoryTree",
  props: ["categories", "counts", "total", "selected"],
  template: "<div />"
});
const BatchBarStub = defineComponent({ name: "DefinitionBatchBar", props: ["selectedCount", "categories"], template: "<div />" });
const DrawerStub = defineComponent({ name: "FactorDetailDrawer", props: ["factorId", "categories"], template: "<div />" });

function mountPage() {
  return mount(DefinitionsPage, {
    global: {
      stubs: {
        FactorDetailDrawer: DrawerStub,
        FactorCategoryTree: TreeStub,
        InfoTip: true,
        "a-table": TableStub,
        "a-table-column": ColumnStub,
        "a-button": defineComponent({
          emits: ["click"],
          template: "<button @click=\"$emit('click', $event)\"><slot /></button>"
        }),
        "a-link": defineComponent({ emits: ["click"], template: "<a @click=\"$emit('click', $event)\"><slot /></a>" }),
        "a-input-search": defineComponent({
          props: ["modelValue"],
          emits: ["update:modelValue", "input", "search"],
          template:
            '<input class="keyword" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value); $emit(\'input\')" />'
        }),
        DefinitionBatchBar: BatchBarStub,
        "a-space": { template: "<div><slot /></div>" },
        "a-tooltip": { template: "<div><slot /></div>" },
        "a-popconfirm": { template: "<div><slot /></div>" },
        "a-drawer": { template: "<div><slot /></div>" }
      }
    }
  });
}

function rowIds(wrapper: ReturnType<typeof mountPage>) {
  return wrapper.findAll(".row").map(row => row.find(".factor-name span").text());
}

describe("factor definitions page", () => {
  beforeEach(() => {
    mocks.listFactors.mockReset().mockResolvedValue({
      factors: [info("Ma", "trend"), info("Ema", "trend"), info("Rsi", "momentum"), info("X")],
      page_result: { has_more: false }
    });
    mocks.listFactorCategories.mockReset().mockResolvedValue([
      { category_id: "trend", name: "趋势类", sort_order: 1 },
      { category_id: "momentum", name: "动量类", sort_order: 2 }
    ]);
    mocks.setFactorCategory.mockReset().mockResolvedValue(undefined);
    mocks.deleteFactorCategory.mockReset().mockResolvedValue(undefined);
    mocks.push.mockReset();
    mocks.route!.query = {};
    mocks.replace.mockReset().mockImplementation(({ query }: { query: Record<string, string> }) => {
      mocks.route!.query = query;
    });
    if (mocks.spaceId) mocks.spaceId.value = "";
  });

  it("waits for the selected space before listing and reloads when it appears", async () => {
    const wrapper = mountPage();
    await flushPromises();
    expect(mocks.listFactors).not.toHaveBeenCalled();

    mocks.spaceId!.value = "crypto";
    await flushPromises();
    expect(mocks.listFactors).toHaveBeenCalledTimes(1);

    mocks.spaceId!.value = "equity";
    await flushPromises();
    expect(mocks.listFactors).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it("分类树按筛选后的结果计数；切换分类写入 ?category= 且关键词仍生效", async () => {
    mocks.spaceId!.value = "crypto";
    const wrapper = mountPage();
    await flushPromises();
    const tree = wrapper.findComponent(TreeStub);
    expect(tree.props("total")).toBe(4);
    expect(tree.props("counts")).toEqual({ trend: 2, momentum: 1, uncategorized: 1 });
    expect(tree.props("categories").map((item: { category_id: string }) => item.category_id)).toEqual([
      "trend",
      "momentum",
      "uncategorized"
    ]);

    await wrapper.get("input.keyword").setValue("ma");
    await wrapper
      .findAll("button")
      .find(button => button.text().includes("查询"))!
      .trigger("click");
    await flushPromises();
    expect(tree.props("counts")).toEqual({ trend: 2 });
    expect(rowIds(wrapper)).toEqual(["Ma", "Ema"]);

    mocks.route!.query = { foo: "bar" };
    tree.vm.$emit("select", "momentum");
    await flushPromises();
    expect(mocks.replace).toHaveBeenLastCalledWith({ path: "/factor/definitions", query: { foo: "bar", category: "momentum" } });
    expect(rowIds(wrapper)).toEqual([]);
    tree.vm.$emit("select", "trend");
    await flushPromises();
    expect(rowIds(wrapper)).toEqual(["Ma", "Ema"]);
    expect((wrapper.get("input.keyword").element as HTMLInputElement).value).toBe("ma");
  });

  it("点行打开详情抽屉，点编辑不打开", async () => {
    mocks.spaceId!.value = "crypto";
    const wrapper = mountPage();
    await flushPromises();
    const drawer = wrapper.findComponent(DrawerStub);
    const firstRow = wrapper.findAll(".row")[0];
    await firstRow.get("button[aria-label='编辑定义']").trigger("click");
    expect(drawer.props("factorId")).toBe("");
    expect(mocks.push).toHaveBeenCalledWith("/factor/definitions/Ma/edit");

    await firstRow.trigger("click");
    expect(drawer.props("factorId")).toBe("Ma");
  });

  it("删除当前选中的非空分类：先迁走其中的因子，再删除并回到全部", async () => {
    mocks.spaceId!.value = "crypto";
    mocks.route!.query = { category: "trend" };
    const wrapper = mountPage();
    await flushPromises();
    wrapper.findComponent(TreeStub).vm.$emit("delete", { category_id: "trend", name: "趋势类", sort_order: 1 });
    await flushPromises();
    expect(mocks.setFactorCategory.mock.calls).toEqual([
      ["Ma", "uncategorized"],
      ["Ema", "uncategorized"]
    ]);
    expect(mocks.deleteFactorCategory).toHaveBeenCalledWith("trend");
    expect(mocks.replace).toHaveBeenLastCalledWith({ path: "/factor/definitions", query: {} });
  });

  it("重命名分类时带上原 sort_order，成功后关闭表单", async () => {
    mocks.spaceId!.value = "crypto";
    mocks.upsertFactorCategory.mockReset().mockResolvedValue({});
    const wrapper = mountPage();
    await flushPromises();
    const done = vi.fn();
    wrapper.findComponent(TreeStub).vm.$emit("rename", { category_id: "trend", name: "趋势类", sort_order: 7 }, "趋势", done);
    await flushPromises();
    expect(mocks.upsertFactorCategory).toHaveBeenCalledWith({ category_id: "trend", name: "趋势", sort_order: 7 });
    expect(done).toHaveBeenCalled();
  });

  it("勾选后批量移动分类：逐个调用并刷新、清空勾选", async () => {
    mocks.spaceId!.value = "crypto";
    const wrapper = mountPage();
    await flushPromises();
    wrapper.findComponent({ name: "DefinitionTable" }).vm.$emit("select", ["Ma", "Rsi"]);
    await flushPromises();
    const bar = wrapper.findComponent(BatchBarStub);
    expect(bar.props("selectedCount")).toBe(2);
    // 其他用例留下的页面实例也会调 listFactors，这里只看这次移动带来的刷新
    const before = mocks.listFactors.mock.calls.length;
    bar.vm.$emit("move", "trend");
    await flushPromises();
    expect(mocks.setFactorCategory.mock.calls).toEqual([
      ["Ma", "trend"],
      ["Rsi", "trend"]
    ]);
    expect(mocks.listFactors.mock.calls.length).toBe(before + 1);
    expect(bar.props("selectedCount")).toBe(0);
  });

  it("迁移中途失败时不删除分类", async () => {
    mocks.spaceId!.value = "crypto";
    mocks.setFactorCategory.mockReset().mockRejectedValueOnce(new Error("boom"));
    const wrapper = mountPage();
    await flushPromises();
    wrapper.findComponent(TreeStub).vm.$emit("delete", { category_id: "trend", name: "趋势类" });
    await flushPromises();
    expect(mocks.setFactorCategory).toHaveBeenCalledTimes(1);
    expect(mocks.deleteFactorCategory).not.toHaveBeenCalled();
  });
});

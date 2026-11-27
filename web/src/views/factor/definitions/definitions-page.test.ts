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
  reloadTasks: vi.fn(),
  push: vi.fn(),
  replace: vi.fn(),
  route: undefined as { path: string; query: Record<string, string> } | undefined,
  spaceId: undefined as { value: string } | undefined,
  storeTasks: undefined as { value: unknown[] } | undefined,
  storeLoading: undefined as { value: boolean } | undefined,
  storeLoadError: undefined as { value: string } | undefined,
  storeLoaded: undefined as { value: boolean } | undefined
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
  const { ref: vueRef, reactive: vueReactive } = await import("vue");
  mocks.spaceId = vueRef("");
  mocks.storeTasks = vueRef<unknown[]>([]);
  mocks.storeLoading = vueRef(false);
  mocks.storeLoadError = vueRef("");
  mocks.storeLoaded = vueRef(true);
  const store = vueReactive({
    get tasks() {
      return mocks.storeTasks!.value;
    },
    get loading() {
      return mocks.storeLoading!.value;
    },
    get loadError() {
      return mocks.storeLoadError!.value;
    },
    reload: mocks.reloadTasks,
    isLoadedFor: () => mocks.storeLoaded!.value,
    taskLabel: (set: { name?: string; freq?: string; task_id: string }) =>
      set.name ? `${set.name} · ${set.freq || ""}` : set.task_id
  });
  return {
    useFactorScope: () => ({
      store,
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
  name: "TableStub",
  props: {
    data: { type: Array, default: () => [] },
    pagination: { type: Object, default: undefined }
  },
  emits: ["row-click", "page-change"],
  setup(props, { slots, emit }) {
    return () =>
      h("div", [
        ...(props.data as FactorInfo[]).map(record =>
          h("div", { class: "row", onClick: () => emit("row-click", record) }, [
            h(RowProvider, { record }, () => slots.columns?.())
          ])
        ),
        !(props.data as unknown[]).length ? h("div", { class: "empty-slot" }, slots.empty?.()) : null
      ]);
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
    mocks.reloadTasks.mockReset().mockResolvedValue(undefined);
    if (mocks.storeTasks) mocks.storeTasks.value = [];
    if (mocks.storeLoading) mocks.storeLoading.value = false;
    if (mocks.storeLoadError) mocks.storeLoadError.value = "";
    if (mocks.storeLoaded) mocks.storeLoaded.value = true;
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
    expect(mocks.route!.query.detail).toBe("Ma");
  });

  it("带 ?detail= 进来时打开那个因子的详情；关掉后 URL 去掉 detail，别的参数保留", async () => {
    mocks.spaceId!.value = "crypto";
    mocks.route!.query = { detail: "Ma", category: "trend" };
    const wrapper = mountPage();
    await flushPromises();
    const drawer = wrapper.findComponent(DrawerStub);
    expect(drawer.props("factorId")).toBe("Ma");
    drawer.vm.$emit("close");
    await flushPromises();
    expect(mocks.route!.query).toEqual({ category: "trend" });
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

  it("切到产出字段页签展示当前空间计算任务展开的结果列名，点因子打开详情，详情可联动回产出字段", async () => {
    mocks.storeTasks!.value = [
      {
        factor_task: {
          task_id: "ftask_swap_1m",
          space_id: "crypto",
          source_dataset_id: "ds_1",
          freq: "1m",
          name: "合约一分钟",
          result_dataset_id: "dataset_factor_swap_1m",
          status: "enabled"
        },
        members: [
          {
            task_id: "ftask_swap_1m",
            factor_id: "Ma",
            status: "enabled",
            factor: info("Ma", "trend").factor,
            created_at: "0",
            updated_at: "0",
            member_key: "ma__5",
            output_columns: ["ma__5"],
            params: { window: 5 },
            kind: "fixed"
          }
        ]
      }
    ];
    mocks.spaceId!.value = "crypto";
    mocks.route!.query = { tab: "fields" };
    const wrapper = mountPage();
    await flushPromises();

    expect(wrapper.text()).toContain("ma__5");
    expect(wrapper.text()).toContain("合约一分钟 · 1m");

    const factorLink = wrapper.findAll("a").find(link => link.text().includes("Ma_module"));
    expect(factorLink).toBeDefined();
    await factorLink!.trigger("click");
    await flushPromises();
    expect(mocks.route!.query.detail).toBe("Ma");

    wrapper.findComponent(DrawerStub).vm.$emit("viewOutputs", "ftask_swap_1m", "ma__5");
    await flushPromises();
    expect(mocks.route!.query).toEqual({ tab: "fields", task: "ftask_swap_1m", keyword: "ma__5" });
    wrapper.unmount();
  });

  it("批量移动分类和删除分类后会静默刷新计算任务，保持产出字段分类同步", async () => {
    mocks.spaceId!.value = "crypto";
    const wrapper = mountPage();
    await flushPromises();

    wrapper.findComponent({ name: "DefinitionTable" }).vm.$emit("select", ["Ma"]);
    await flushPromises();
    wrapper.findComponent(BatchBarStub).vm.$emit("move", "momentum");
    await flushPromises();
    expect(mocks.reloadTasks).toHaveBeenCalledWith({ silent: true });

    mocks.reloadTasks.mockClear();
    wrapper.findComponent(TreeStub).vm.$emit("delete", { category_id: "trend", name: "趋势类", sort_order: 1 });
    await flushPromises();
    expect(mocks.reloadTasks).toHaveBeenCalledWith({ silent: true });
    wrapper.unmount();
  });

  it("产出字段页签打开或关闭详情抽屉不重置页码；切空间后从详情跳入保留任务与关键词筛选", async () => {
    const taskCrypto = {
      factor_task: {
        task_id: "ftask_crypto",
        space_id: "crypto",
        source_dataset_id: "ds_1",
        freq: "1m",
        name: "币安一分钟",
        result_dataset_id: "dataset_factor_crypto",
        status: "enabled"
      },
      members: Array.from({ length: 25 }, (_, i) => ({
        task_id: "ftask_crypto",
        factor_id: "Ma",
        status: "enabled",
        factor: info("Ma", "trend").factor,
        created_at: "0",
        updated_at: "0",
        member_key: `ma__${i + 1}`,
        output_columns: [`ma__${i + 1}`],
        params: { window: i + 1 },
        kind: "fixed"
      }))
    };
    const taskStock = {
      factor_task: {
        task_id: "ftask_stock",
        space_id: "stockcn",
        source_dataset_id: "ds_2",
        freq: "1d",
        name: "A股日线",
        result_dataset_id: "dataset_factor_stock",
        status: "enabled"
      },
      members: [
        {
          task_id: "ftask_stock",
          factor_id: "Rsi",
          status: "enabled",
          factor: info("Rsi", "momentum").factor,
          created_at: "0",
          updated_at: "0",
          member_key: "rsi__14",
          output_columns: ["rsi__14"],
          params: { window: 14 },
          kind: "fixed"
        }
      ]
    };

    const taskStockOther = {
      factor_task: {
        task_id: "ftask_stock_other",
        space_id: "stockcn",
        source_dataset_id: "ds_3",
        freq: "1w",
        name: "A股周线",
        result_dataset_id: "dataset_factor_stock_other",
        status: "enabled"
      },
      members: [
        {
          task_id: "ftask_stock_other",
          factor_id: "Ma",
          status: "enabled",
          factor: info("Ma", "trend").factor,
          created_at: "0",
          updated_at: "0",
          member_key: "bias__20",
          output_columns: ["bias__20"],
          params: { window: 20 },
          kind: "fixed"
        }
      ]
    };

    mocks.storeTasks!.value = [taskCrypto];
    mocks.spaceId!.value = "crypto";
    mocks.route!.query = { tab: "fields" };
    const wrapper = mountPage();
    await flushPromises();

    // 翻至第 2 页后打开与关闭因子详情抽屉，页码保持第 2 页
    const table = wrapper.findComponent({ name: "TableStub" });
    table.vm.$emit("page-change", 2);
    await flushPromises();
    expect((table.props("pagination") as { current: number }).current).toBe(2);

    const factorLink = wrapper.findAll("a").find(link => link.text().includes("Ma_module"));
    await factorLink!.trigger("click");
    await flushPromises();
    expect(mocks.route!.query.detail).toBe("Ma");
    expect((table.props("pagination") as { current: number }).current).toBe(2);

    wrapper.findComponent(DrawerStub).vm.$emit("close");
    await flushPromises();
    expect(mocks.route!.query.detail).toBeUndefined();
    expect((table.props("pagination") as { current: number }).current).toBe(2);

    // 切回因子定义页签（使 OutputFieldsTab 失活），再切到 stockcn，最后从详情抽屉点「产出字段」跳入
    const defTab = wrapper.findAll("[role='tab']").find(btn => btn.text().includes("因子定义"));
    await defTab!.trigger("click");
    await flushPromises();

    mocks.storeTasks!.value = [taskStock, taskStockOther];
    mocks.spaceId!.value = "stockcn";
    await flushPromises();

    wrapper.findComponent(DrawerStub).vm.$emit("viewOutputs", "ftask_stock", "rsi__14");
    await flushPromises();
    expect(mocks.route!.query).toEqual({ tab: "fields", task: "ftask_stock", keyword: "rsi__14" });
    expect(wrapper.text()).toContain("rsi__14");
    expect(wrapper.text()).not.toContain("bias__20");
    wrapper.unmount();
  });

  it("产出字段页签自动清理无效分类参数，且加载失败时不误显前往计算任务按钮", async () => {
    mocks.spaceId!.value = "crypto";
    mocks.route!.query = { tab: "fields", category: "ghost_cat" };
    mocks.storeLoadError!.value = "rpc timeout";
    const wrapper = mountPage();
    await flushPromises();

    expect(wrapper.text()).toContain("计算任务加载失败");
    expect(wrapper.text()).not.toContain("前往计算任务");
    wrapper.unmount();

    mocks.storeLoadError!.value = "";
    mocks.route!.query = { tab: "fields", category: "ghost_cat", task: "ghost_task" };
    const wrapper2 = mountPage();
    await flushPromises();
    expect(mocks.route!.query.category).toBeUndefined();
    expect(mocks.route!.query.task).toBeUndefined();
    wrapper2.unmount();
  });
});

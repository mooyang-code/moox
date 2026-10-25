import { flushPromises, shallowMount } from "@vue/test-utils";
import { defineComponent, reactive } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FactorMember, FactorSetInfo } from "@/api/factor/types";

const hoisted = vi.hoisted(() => ({
  route: { path: "/factor/tasks", query: {} as Record<string, string | undefined> },
  navigate: vi.fn(),
  selectSet: vi.fn(),
  listViews: vi.fn(),
  listAllViewColumns: vi.fn(),
  listFactorCategories: vi.fn(),
  getDisplay: vi.fn(),
  warning: vi.fn(),
  store: {} as Record<string, unknown>
}));

vi.mock("vue-router", () => ({ useRoute: () => hoisted.route, useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/views/factor/shared/use-factor-scope", () => ({
  useFactorScope: () => ({
    store: hoisted.store,
    spaceId: { value: "crypto" },
    selectSet: hoisted.selectSet,
    navigate: hoisted.navigate,
    reload: vi.fn()
  })
}));
vi.mock("@/api/storage/metadata", () => ({ listViews: hoisted.listViews, listAllViewColumns: hoisted.listAllViewColumns }));
vi.mock("@/api/factor", () => ({ listFactorCategories: hoisted.listFactorCategories }));
vi.mock("@/api/strategy", () => ({ getStrategyDisplaySettings: hoisted.getDisplay }));
vi.mock("@arco-design/web-vue", () => ({ Message: { error: vi.fn(), warning: hoisted.warning } }));

import FactorResults from "./index.vue";

function member(alias: string, outputs: string[], category = "momentum"): FactorMember {
  return {
    set_id: "a",
    member_alias: alias,
    factor_id: alias,
    status: "enabled",
    factor: {
      factor_id: alias,
      name: alias,
      spec_hash: "",
      factor_type: "timeseries",
      input_columns: [],
      outputs,
      lookback_periods: 1,
      allow_partial_universe: false,
      category_id: category,
      created_at: "",
      updated_at: ""
    },
    created_at: "",
    updated_at: "",
    output_columns: outputs,
    param_columns: [`${alias}__window`]
  };
}

function setInfo(setId: string, ownerInstanceId = ""): FactorSetInfo {
  return {
    factor_set: {
      set_id: setId,
      space_id: "crypto",
      source_dataset_id: "kline",
      freq: "1m",
      result_dataset_id: `r_${setId}`,
      status: "enabled",
      owner_instance_id: ownerInstanceId
    },
    members: [member("rsi", ["rsi"]), member("boll", ["boll_mid", "boll_up"], "trend")]
  };
}

const column = (name: string, attributes?: Record<string, string>) => ({
  space_id: "crypto",
  view_id: "view_a",
  column_name: name,
  origin_type: "COLUMN_ORIGIN_TYPE_DATASET_COLUMN",
  origin_id: name,
  value_type: "FIELD_VALUE_TYPE_DOUBLE",
  attributes
});

const viewColumns = [
  column("open"),
  column("close"),
  column("request_id"),
  column("rsi", { column_kind: "output", member_alias: "rsi" }),
  column("rsi__window", { column_kind: "param", member_alias: "rsi" }),
  column("boll_mid", { column_kind: "output", member_alias: "boll" }),
  column("boll_up", { column_kind: "output", member_alias: "boll" }),
  column("boll__window", { column_kind: "param", member_alias: "boll" })
];

const ViewBrowseStub = defineComponent({
  name: "ViewBrowse",
  props: ["queryReady", "displayColumns", "projectColumns", "searchColumns", "extraFilters"],
  template: '<div class="view-browse-stub"><slot name="query-extra" /></div>'
});

function mountPage() {
  return shallowMount(FactorResults, {
    global: {
      stubs: {
        ViewBrowse: ViewBrowseStub,
        FactorConditionBar: defineComponent({ name: "FactorConditionBar", props: ["conditions", "scope"], template: "<div />" })
      }
    }
  });
}

describe("因子结果页", () => {
  beforeEach(() => {
    window.localStorage.clear();
    hoisted.route.query = { tab: "results", set: "a" };
    hoisted.navigate.mockReset();
    hoisted.warning.mockReset();
    Object.assign(
      hoisted.store,
      reactive({
        sets: [setInfo("a")],
        currentSetId: "a",
        loading: false,
        loadError: "",
        setLabel: () => "K线 · 1m",
        reload: vi.fn()
      })
    );
    hoisted.listViews.mockReset().mockResolvedValue({
      views: [{ view_id: "view_a", space_id: "crypto", dataset_id: "r_a", attributes: { view_role: "factor_result" } }]
    });
    hoisted.listAllViewColumns.mockReset().mockResolvedValue(viewColumns);
    hoisted.listFactorCategories.mockReset().mockResolvedValue([
      { category_id: "trend", name: "趋势类" },
      { category_id: "momentum", name: "动量类" }
    ]);
    hoisted.getDisplay.mockReset().mockResolvedValue({ categories: [], members: [], columns: [] });
  });

  it("列元数据就绪前不放行查询；就绪后只投影行情列，搜索只含行情列", async () => {
    let resolveColumns!: (value: unknown) => void;
    hoisted.listAllViewColumns.mockReturnValue(new Promise(resolve => (resolveColumns = resolve)));
    const wrapper = mountPage();
    await flushPromises();
    const browse = () => wrapper.findComponent(ViewBrowseStub);
    expect(browse().props("queryReady")).toBe(false);

    resolveColumns(viewColumns);
    await flushPromises();
    expect(browse().props("queryReady")).toBe(true);
    expect(browse().props("projectColumns")).toEqual(["open", "close"]);
    expect(browse().props("searchColumns")).toEqual(["open", "close"]);
    expect(browse().props("displayColumns")).toEqual({
      system: ["subject_id", "freq", "series_tag", "data_time"],
      business: ["open", "close"]
    });
    expect(browse().props("extraFilters")).toEqual([]);
  });

  it("带 ?fc= 进入时首查就带条件；失效条件被丢弃、提示并改写 URL", async () => {
    hoisted.route.query = {
      tab: "results",
      set: "a",
      fc: JSON.stringify([
        ["rsi", "gt", 70],
        ["gone", "lt", 1]
      ])
    };
    const wrapper = mountPage();
    await flushPromises();
    const browse = wrapper.findComponent(ViewBrowseStub);
    expect(browse.props("queryReady")).toBe(true);
    expect(browse.props("extraFilters")).toEqual([
      { fieldName: "rsi", operator: "gt", value: "70", valueType: "FIELD_VALUE_TYPE_DOUBLE" }
    ]);
    expect(hoisted.warning).toHaveBeenCalledWith("已忽略 1 个失效的因子条件");
    expect(hoisted.navigate).toHaveBeenCalledWith({ fc: JSON.stringify([["rsi", "gt", 70]]) });
  });

  it("修改因子条件写回 ?fc=，清空后去掉", async () => {
    const wrapper = mountPage();
    await flushPromises();
    const bar = wrapper.findComponent({ name: "FactorConditionBar" });
    bar.vm.$emit("update:conditions", [{ column: "boll_mid", op: "range", min: 0, max: 1 }]);
    await flushPromises();
    expect(hoisted.navigate).toHaveBeenLastCalledWith({ fc: JSON.stringify([["boll_mid", "range", 0, 1]]) });
    expect(wrapper.findComponent(ViewBrowseStub).props("extraFilters")).toHaveLength(1);

    hoisted.route.query = { ...hoisted.route.query, fc: JSON.stringify([["boll_mid", "range", 0, 1]]) };
    wrapper.findComponent(ViewBrowseStub).vm.$emit("cleared");
    await flushPromises();
    expect(hoisted.navigate).toHaveBeenLastCalledWith({ fc: undefined });
    expect(wrapper.findComponent(ViewBrowseStub).props("extraFilters")).toEqual([]);
  });

  it("读取保存的列设置：钉住的因子列进入显示与投影，行情列按设置显示", async () => {
    window.localStorage.setItem(
      "moox.factor-results.columns.a",
      JSON.stringify({ hiddenSystem: ["freq"], market: ["close", "request_id"], pinned: ["boll_up", "gone"] })
    );
    const wrapper = mountPage();
    await flushPromises();
    const browse = wrapper.findComponent(ViewBrowseStub);
    expect(browse.props("displayColumns")).toEqual({
      system: ["subject_id", "series_tag", "data_time"],
      business: ["close", "request_id", "boll_up"]
    });
    expect(browse.props("projectColumns")).toEqual(["close", "request_id", "boll_up"]);
    expect(browse.props("searchColumns")).toEqual(["close", "request_id"]);
  });

  it("策略私有因子集按外显设置收窄范围：隐藏的分类不能用于条件", async () => {
    Object.assign(hoisted.store, { sets: [setInfo("a", "inst-1")] });
    hoisted.getDisplay.mockResolvedValue({ categories: ["momentum"], members: [], columns: ["open"] });
    hoisted.route.query = { tab: "results", set: "a", fc: JSON.stringify([["rsi", "gt", 70]]) };
    const wrapper = mountPage();
    await flushPromises();
    const browse = wrapper.findComponent(ViewBrowseStub);
    expect(hoisted.getDisplay).toHaveBeenCalledWith("inst-1");
    expect(browse.props("extraFilters")).toEqual([]);
    expect(browse.props("projectColumns")).toEqual(["close"]);
    const scope = wrapper.findComponent({ name: "FactorConditionBar" }).props("scope");
    expect(scope.outputs.map((item: { column: string }) => item.column)).toEqual(["boll_mid", "boll_up"]);
  });
});

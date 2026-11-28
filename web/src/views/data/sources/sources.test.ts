import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DataSource } from "@/api/storage/types";

const routeMocks = vi.hoisted(() => ({
  replace: vi.fn(),
  route: undefined as { path: string; query: Record<string, string> } | undefined
}));

vi.mock("vue-router", async () => {
  const { reactive } = await import("vue");
  routeMocks.route = reactive({ path: "/data/sources", query: {} as Record<string, string> });
  return {
    useRoute: () => routeMocks.route,
    useRouter: () => ({ replace: routeMocks.replace, push: vi.fn() })
  };
});

const api = vi.hoisted(() => ({
  listDataSources: vi.fn(async () => ({ data_sources: [], page_result: { total: 0 } })),
  upsertDataSource: vi.fn(async () => ({})),
  listAllDatasets: vi.fn(async () => [
    {
      space_id: "crypto",
      dataset_id: "dataset_crypto_kline_1m",
      data_source_id: "binance",
      name: "现货 1m",
      data_kind: "DATA_KIND_TIME_SERIES",
      frequency: "1m",
      status: "active",
      attributes: { owner_module: "collector", dataset_role: "raw_collection" }
    },
    {
      space_id: "crypto",
      dataset_id: "dataset_crypto_custom_record",
      data_source_id: "manual",
      name: "自定义记录",
      data_kind: "DATA_KIND_RECORD",
      status: "active"
    },
    {
      space_id: "crypto",
      dataset_id: "dataset_factor_spot_1m",
      data_source_id: "factor",
      name: "因子结果",
      data_kind: "DATA_KIND_TIME_SERIES",
      frequency: "1m",
      status: "active",
      attributes: { owner_module: "factor", dataset_role: "factor_result" }
    }
  ]),
  listAllDatasetColumns: vi.fn(async (_spaceId: string, datasetId: string) => {
    if (datasetId === "dataset_crypto_kline_1m") {
      return [
        {
          space_id: "crypto",
          dataset_id: datasetId,
          column_name: "close",
          value_type: "FIELD_VALUE_TYPE_DOUBLE",
          display_name: "收盘价",
          group: "行情价格",
          status: "active"
        }
      ];
    }
    if (datasetId === "dataset_crypto_custom_record") {
      return [
        {
          space_id: "crypto",
          dataset_id: datasetId,
          column_name: "custom_score",
          value_type: "FIELD_VALUE_TYPE_INT",
          display_name: "自定义评分",
          group: "自定义",
          status: "active"
        }
      ];
    }
    return [
      {
        space_id: "crypto",
        dataset_id: datasetId,
        column_name: "ma__5",
        value_type: "FIELD_VALUE_TYPE_DOUBLE",
        display_name: "MA5",
        status: "active"
      }
    ];
  })
}));
vi.mock("@/api/storage/metadata", () => api);

const collector = vi.hoisted(() => ({
  GetDataTypeConfigs: vi.fn(async () => ({
    configs: [
      {
        data_type: "kline",
        type_name: "K 线",
        base_output_fields: [
          { field_id: "open", display_name: "开盘价", group: "行情价格", value_type: "double" },
          { field_id: "close", display_name: "收盘价", group: "行情价格", value_type: "double" }
        ],
        extra_fields: [
          {
            field_id: "funding_rate",
            display_name: "资金费率",
            group: "合约指标",
            market_id: "crypto",
            instrument_type: "swap",
            frequencies: ["1h", "1d"],
            provider_id: "binance",
            value_type: "double",
            fill: "zero"
          },
          {
            field_id: "adj_factor",
            display_name: "后复权因子",
            group: "复权",
            market_id: "stockcn",
            instrument_type: "equity",
            frequencies: ["1d"],
            provider_id: "sina_hfq",
            value_type: "double",
            fill: "prev"
          }
        ]
      }
    ]
  }))
}));
vi.mock("@/api/collector", () => collector);
vi.mock("@/api/storage/spaces", () => ({
  listSpaces: vi.fn(async () => ({
    spaces: [
      { space_id: "crypto", name: "crypto", status: "active", timezone: "UTC" },
      { space_id: "stockcn", name: "stockcn", status: "active", timezone: "Asia/Shanghai" }
    ]
  }))
}));
vi.mock("@/api/http", () => ({ reportError: vi.fn() }));

import { useSpaceStore } from "@/store/modules/space";
import SourcesPage from "./index.vue";
import SourcesTab from "./sources-tab.vue";
import { buildExtraFieldSources } from "./extra-field-sources";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

interface SourcesTabVM {
  openCreate(): void;
  visible: boolean;
  form: DataSource;
}

let wrapper: VueWrapper;
beforeEach(() => {
  setActivePinia(createPinia());
  routeMocks.route!.query = {};
  routeMocks.replace.mockReset().mockImplementation(async ({ query }: { query: Record<string, string> }) => {
    routeMocks.route!.query = query;
  });
  api.upsertDataSource.mockReset().mockResolvedValue({});
  api.listAllDatasetColumns.mockClear();
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

async function mountSourcesPage(space = "crypto", query: Record<string, string> = {}) {
  routeMocks.route!.query = { ...query };
  const store = useSpaceStore();
  await store.loadSpaces();
  store.setSelectedSpace(space);
  wrapper = mount(SourcesPage, { global: { plugins: [ArcoVue] }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

async function openCreateDialog() {
  await mountSourcesPage("crypto");
  const vm = wrapper.findComponent(SourcesTab).vm as unknown as SourcesTabVM;
  vm.openCreate();
  await flushPromises();
  return vm;
}

/** 点弹窗的“确定”，走真实的 on-before-ok */
async function clickOk() {
  document.querySelector<HTMLElement>(".arco-modal-footer .arco-btn-primary")!.click();
  await flushPromises();
}

describe("数据源弹窗", () => {
  it("校验没过时点确定，弹窗不关、不发请求", async () => {
    const vm = await openCreateDialog();
    vm.form.name = "币安";
    await clickOk();
    expect(api.upsertDataSource).not.toHaveBeenCalled();
    expect(vm.visible).toBe(true);
    expect(vm.form.name).toBe("币安");
  });

  it("保存失败时弹窗不关，填的内容还在；成功后才关", async () => {
    const vm = await openCreateDialog();
    Object.assign(vm.form, { data_source_id: "binance", name: "币安" });
    api.upsertDataSource.mockRejectedValueOnce(new Error("服务不可用"));
    await clickOk();
    expect(api.upsertDataSource).toHaveBeenCalledTimes(1);
    expect(api.upsertDataSource).toHaveBeenLastCalledWith(
      expect.objectContaining({ space_id: "crypto", data_source_id: "binance", name: "币安" }),
      true
    );
    expect(vm.visible).toBe(true);
    expect(vm.form.data_source_id).toBe("binance");

    await clickOk();
    expect(api.upsertDataSource).toHaveBeenCalledTimes(2);
    expect(vm.visible).toBe(false);
  });
});

describe("附加字段来源与字段定义页签", () => {
  const options = [
    {
      field_id: "adj_factor",
      display_name: "后复权因子",
      market_id: "stockcn",
      instrument_type: "equity",
      frequencies: ["1d"],
      provider_id: "sina_hfq"
    },
    {
      field_id: "sw_l1",
      display_name: "申万一级行业",
      market_id: "stockcn",
      instrument_type: "equity",
      frequencies: ["1d"],
      provider_id: "swsresearch"
    },
    { field_id: "sw_l2", market_id: "stockcn", instrument_type: "equity", frequencies: ["1d"], provider_id: "swsresearch" },
    {
      field_id: "fin_revenue",
      market_id: "stockcn",
      instrument_type: "equity",
      frequencies: ["1d"],
      provider_id: "sina_finance"
    },
    {
      field_id: "funding_rate",
      display_name: "资金费率",
      market_id: "crypto",
      instrument_type: "swap",
      frequencies: ["1h", "1d"],
      provider_id: "binance"
    }
  ];

  it("按提供方归并当前空间的附加字段，带中文名和各自的字段", () => {
    expect(buildExtraFieldSources(options, "stockcn")).toEqual([
      {
        provider_id: "sina_hfq",
        name: "复权因子（新浪）",
        instruments: "股票",
        frequencies: "1d",
        fields: [{ field_id: "adj_factor", name: "后复权因子" }]
      },
      {
        provider_id: "swsresearch",
        name: "申万行业",
        instruments: "股票",
        frequencies: "1d",
        fields: [
          { field_id: "sw_l1", name: "申万一级行业" },
          { field_id: "sw_l2", name: "sw_l2" }
        ]
      },
      {
        provider_id: "sina_finance",
        name: "财务报表（新浪）",
        instruments: "股票",
        frequencies: "1d",
        fields: [{ field_id: "fin_revenue", name: "fin_revenue" }]
      }
    ]);
    expect(buildExtraFieldSources(options, "crypto").map(row => [row.name, row.instruments, row.frequencies])).toEqual([
      ["资金费率（币安）", "永续", "1h、1d"]
    ]);
    expect(buildExtraFieldSources(options, "other")).toEqual([]);
  });

  it("数据源页签展示精简版附加字段来源，点击查看跳转到字段定义页签并按提供方筛选", async () => {
    await mountSourcesPage("crypto");
    const section = wrapper.find(".extra-sources");
    expect(section.exists()).toBe(true);
    expect(section.text()).toContain("资金费率（币安）");
    expect(section.text()).toContain("共 1 个字段");
    expect(section.text()).not.toContain("复权因子");

    const viewLink = section.findAll(".arco-link").find(link => link.text().includes("查看"));
    expect(viewLink).toBeDefined();
    await viewLink!.trigger("click");
    await flushPromises();

    expect(routeMocks.route!.query).toEqual({ tab: "fields", provider: "binance" });
    expect(wrapper.text()).toContain("来源：资金费率（币安）");
    expect(wrapper.text()).toContain("funding_rate");
    expect(wrapper.text()).not.toContain("open");
  });

  it("字段定义页签聚合原始基础、附加与非因子数据集列，自动排除因子结果数据集列", async () => {
    await mountSourcesPage("crypto", { tab: "fields" });

    // 只读取非因子数据集的列，不读 dataset_factor_spot_1m
    expect(api.listAllDatasetColumns).toHaveBeenCalledWith("crypto", "dataset_crypto_kline_1m");
    expect(api.listAllDatasetColumns).toHaveBeenCalledWith("crypto", "dataset_crypto_custom_record");
    expect(api.listAllDatasetColumns).not.toHaveBeenCalledWith("crypto", "dataset_factor_spot_1m");

    const text = wrapper.text();
    expect(text).toContain("开盘价");
    expect(text).toContain("收盘价");
    expect(text).toContain("资金费率");
    expect(text).toContain("补零");
    expect(text).toContain("自定义评分");
    expect(text).not.toContain("ma__5");
    expect(text).not.toContain("后复权因子");

    // 点击左侧分组切换过滤
    const groupButtons = wrapper.findAll(".group-filter-panel .group-item");
    const customGroup = groupButtons.find(btn => btn.text().includes("自定义"));
    expect(customGroup).toBeDefined();
    await customGroup!.trigger("click");
    await flushPromises();
    expect(routeMocks.route!.query.group).toBe("自定义");
    expect(wrapper.find(".field-total").text()).toContain("共 1 个字段");
  });

  it("切空间重建页面后从附加字段来源跳入字段定义，保留提供方筛选并加载新空间字段", async () => {
    await mountSourcesPage("crypto", { tab: "fields" });
    expect(wrapper.text()).toContain("资金费率");

    // 模拟主视图切空间重建：旧页面（含内部 keep-alive）卸载，按新空间重新挂载
    wrapper.unmount();
    await mountSourcesPage("stockcn", {});

    // 点击 stockcn 的附加字段来源「查看 →」跳入字段定义页签
    const viewLink = wrapper
      .find(".extra-sources")
      .findAll(".arco-link")
      .find(link => link.text().includes("查看"));
    expect(viewLink).toBeDefined();
    await viewLink!.trigger("click");
    await flushPromises();

    expect(routeMocks.route!.query).toEqual({ tab: "fields", provider: "sina_hfq" });
    expect(wrapper.text()).toContain("来源：复权因子（新浪）");
    expect(wrapper.text()).toContain("后复权因子");
    expect(wrapper.text()).not.toContain("开盘价");
  });

  it("在字段定义输入防抖期间切回数据源页签，不会在防抖触发后强行弹回字段定义", async () => {
    vi.useFakeTimers();
    try {
      await mountSourcesPage("crypto", { tab: "fields" });
      const searchInput = wrapper.find(".keyword-input input");
      await searchInput.setValue("funding");
      await searchInput.trigger("input");

      const sourceTabBtn = wrapper.findAll("[role='tab']").find(btn => btn.text().includes("数据源"));
      await sourceTabBtn!.trigger("click");
      await flushPromises();
      expect(routeMocks.route!.query).toEqual({});

      vi.advanceTimersByTime(300);
      await flushPromises();
      expect(routeMocks.route!.query).toEqual({});
      expect(wrapper.find(".sources-tab").exists()).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("URL 携带不存在的分组时（首载及激活态路由变更）自动回退全部；双接口均失败时提示不矛盾", async () => {
    await mountSourcesPage("crypto", { tab: "fields", group: "不存在的分组" });
    expect(routeMocks.route!.query.group).toBeUndefined();
    expect(wrapper.find(".field-total").text()).toContain("共 4 个字段");

    // 激活态经浏览器前进/后退跳入非法分组也应立即回退
    routeMocks.route!.query = { tab: "fields", group: "历史失效分组" };
    await flushPromises();
    expect(routeMocks.route!.query.group).toBeUndefined();
    expect(wrapper.find(".field-total").text()).toContain("共 4 个字段");
    wrapper.unmount();

    collector.GetDataTypeConfigs.mockRejectedValueOnce(new Error("collector down"));
    api.listAllDatasets.mockRejectedValueOnce(new Error("storage down"));
    await mountSourcesPage("crypto", { tab: "fields" });
    expect(wrapper.find(".field-warning-alert").text()).toContain("采集字段规格与当前空间数据集列均加载失败");
    expect(wrapper.find(".field-warning-alert").text()).not.toContain("仅展示采集规格字段");
  });
});

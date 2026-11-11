import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DataSource } from "@/api/storage/types";

const api = vi.hoisted(() => ({
  listDataSources: vi.fn(async () => ({ data_sources: [], page_result: { total: 0 } })),
  createDataSource: vi.fn(async () => ({})),
  updateDataSource: vi.fn(async () => ({})),
  listFields: vi.fn(async () => ({
    fields: [{ field_id: "funding_rate", name: "资金费率" }],
    page_result: { has_more: false }
  }))
}));
vi.mock("@/api/storage/metadata", () => api);
const collector = vi.hoisted(() => ({
  GetDataTypeConfigWithFields: vi.fn(async () => ({
    detail: {
      extra_fields: [
        {
          field_id: "funding_rate",
          market_id: "crypto",
          instrument_type: "swap",
          frequencies: ["1h", "1d"],
          provider_id: "binance"
        },
        { field_id: "adj_factor", market_id: "stockcn", instrument_type: "equity", frequencies: ["1d"], provider_id: "sina_hfq" }
      ]
    }
  }))
}));
vi.mock("@/api/collector", () => collector);
vi.mock("@/api/admin/spaces", () => ({
  listSpaces: vi.fn(async () => ({ spaces: [{ space_id: "crypto", name: "crypto", status: "active", timezone: "UTC" }] }))
}));
vi.mock("@/api/admin/http", () => ({ reportControlError: vi.fn() }));
import { useSpaceStore } from "@/store/modules/space";
import SourcesPage from "./index.vue";
import { buildExtraFieldSources } from "./extra-field-sources";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

interface PageVM {
  openCreate(): void;
  visible: boolean;
  form: DataSource;
}

let wrapper: VueWrapper;
beforeEach(() => {
  setActivePinia(createPinia());
  api.createDataSource.mockReset().mockResolvedValue({});
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

async function openCreateDialog() {
  const store = useSpaceStore();
  await store.loadSpaces();
  store.setSelectedSpace("crypto");
  wrapper = mount(SourcesPage, { global: { plugins: [ArcoVue] }, attachTo: document.body });
  await flushPromises();
  const vm = wrapper.vm as unknown as PageVM;
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
    expect(api.createDataSource).not.toHaveBeenCalled();
    expect(vm.visible).toBe(true);
    expect(vm.form.name).toBe("币安");
  });

  it("保存失败时弹窗不关，填的内容还在；成功后才关", async () => {
    const vm = await openCreateDialog();
    Object.assign(vm.form, { data_source_id: "binance", name: "币安" });
    api.createDataSource.mockRejectedValueOnce(new Error("服务不可用"));
    await clickOk();
    expect(api.createDataSource).toHaveBeenCalledTimes(1);
    expect(vm.visible).toBe(true);
    expect(vm.form.data_source_id).toBe("binance");

    await clickOk();
    expect(api.createDataSource).toHaveBeenCalledTimes(2);
    expect(vm.visible).toBe(false);
  });
});

describe("附加字段来源", () => {
  const options = [
    { field_id: "adj_factor", market_id: "stockcn", instrument_type: "equity", frequencies: ["1d"], provider_id: "sina_hfq" },
    { field_id: "sw_l1", market_id: "stockcn", instrument_type: "equity", frequencies: ["1d"], provider_id: "swsresearch" },
    { field_id: "sw_l2", market_id: "stockcn", instrument_type: "equity", frequencies: ["1d"], provider_id: "swsresearch" },
    {
      field_id: "fin_revenue",
      market_id: "stockcn",
      instrument_type: "equity",
      frequencies: ["1d"],
      provider_id: "sina_finance"
    },
    { field_id: "funding_rate", market_id: "crypto", instrument_type: "swap", frequencies: ["1h", "1d"], provider_id: "binance" }
  ];

  it("按提供方归并当前空间的附加字段，带中文名和各自的字段", () => {
    const names = new Map([
      ["adj_factor", "后复权因子"],
      ["sw_l1", "申万一级行业"]
    ]);
    expect(buildExtraFieldSources(options, "stockcn", names)).toEqual([
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
          // 字段目录里没有的显示字段 ID
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
    expect(buildExtraFieldSources(options, "crypto", new Map()).map(row => [row.name, row.instruments, row.frequencies])).toEqual(
      [["资金费率（币安）", "永续", "1h、1d"]]
    );
    expect(buildExtraFieldSources(options, "other", new Map())).toEqual([]);
  });

  it("数据源页面列出当前空间的附加字段来源", async () => {
    const store = useSpaceStore();
    await store.loadSpaces();
    store.setSelectedSpace("crypto");
    wrapper = mount(SourcesPage, { global: { plugins: [ArcoVue] }, attachTo: document.body });
    await flushPromises();
    const section = wrapper.find(".extra-sources");
    expect(section.exists()).toBe(true);
    expect(section.text()).toContain("资金费率（币安）");
    expect(section.text()).toContain("funding_rate");
    // 别的空间的来源不出现
    expect(section.text()).not.toContain("复权因子");
  });
});

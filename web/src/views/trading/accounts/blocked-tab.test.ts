import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BlockedInstrument } from "@/api/trade";
import { useSpaceStore } from "@/store/modules/space";

const api = vi.hoisted(() => ({ unblockInstrument: vi.fn() }));
vi.mock("@/api/trade", async () => ({ ...(await vi.importActual<typeof import("@/api/trade")>("@/api/trade")), ...api }));
import BlockedTab from "./blocked-tab.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const rows: BlockedInstrument[] = [
  {
    exchange: "binance",
    market_type: "spot",
    exchange_symbol: "LUNAUSDT",
    instrument_id: "LUNA-USDT-SPOT",
    base_asset: "LUNA",
    reason: "币安公告下架",
    blocked_at: "1700000000000",
    scope: "ALL",
    delist_id: "delist-1"
  },
  {
    exchange: "okx",
    market_type: "swap",
    exchange_symbol: "FTT-USDT-SWAP",
    instrument_id: "",
    base_asset: "",
    reason: "",
    blocked_at: "1700000100000",
    scope: "PAPER_ONLY",
    delist_id: ""
  }
];

interface TabVM {
  askUnblock(item: BlockedInstrument): void;
  confirmVisible: boolean;
}

let wrapper: VueWrapper;
const mountTab = (list = rows) => {
  wrapper = mount(BlockedTab, { props: { rows: list, loading: false }, global: { plugins: [ArcoVue] }, attachTo: document.body });
  return wrapper;
};
const confirmButton = (text: string) =>
  [...document.querySelectorAll<HTMLButtonElement>(".tx-confirm button")].find(item => item.textContent?.trim() === text);
const confirmInput = () => document.querySelector<HTMLInputElement>(".tx-confirm input");
const typeConfirmName = async (value: string) => {
  const input = confirmInput();
  if (!input) throw new Error("未找到确认输入框");
  input.value = value;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  await flushPromises();
};

beforeEach(() => {
  setActivePinia(createPinia());
  api.unblockInstrument.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, removed: true });
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

describe("下架名单子页签", () => {
  it("列出交易所、市场、代码、范围、原因和下架记录，缺的字段写“—”", async () => {
    mountTab();
    await flushPromises();
    const text = wrapper.text();
    expect(text).toContain("LUNAUSDT");
    expect(text).toContain("现货");
    expect(text).toContain("币安");
    expect(text).toContain("币安公告下架");
    expect(text).toContain("delist-1");
    expect(text).toContain("全部");
    expect(text).toContain("仅模拟盘");
    expect(text).toContain("FTT-USDT-SWAP");
    expect(text).toContain("永续");
    expect(text).toContain("OKX");
    expect(wrapper.findAll("tbody tr")).toHaveLength(2);
  });

  it("写入时间统一按 UTC 显示，不受当前空间时区影响", async () => {
    const spaceStore = useSpaceStore();
    spaceStore.spaces = [{ space_id: "stockcn", name: "A 股", display_zone: "Asia/Shanghai" }] as typeof spaceStore.spaces;
    spaceStore.selectedSpaceId = "stockcn";
    mountTab();
    await flushPromises();
    expect(wrapper.text()).toContain("2023-11-14 22:13:20 UTC");
    expect(wrapper.text()).not.toContain("2023-11-15 06:13:20");
  });

  it("点下架记录通知页面定位到上段对应的记录", async () => {
    mountTab();
    await flushPromises();
    await wrapper.find("tbody .arco-link").trigger("click");
    expect(wrapper.emitted("locate")).toEqual([["delist-1"]]);
  });

  it("名单为空时说明没有被禁止买入的标的", async () => {
    mountTab([]);
    await flushPromises();
    expect(wrapper.text()).toContain("没有被禁止买入的标的");
  });

  it("实盘范围（ALL）解除必须走生产级高危确认，输入标的代码后才能提交", async () => {
    mountTab();
    await flushPromises();
    const buttons = wrapper.findAll("tbody button").filter(item => item.text() === "解除");
    await buttons[0].trigger("click");
    await flushPromises();
    expect((wrapper.vm as unknown as TabVM).confirmVisible).toBe(true);
    expect(api.unblockInstrument).not.toHaveBeenCalled();
    expect(document.querySelector(".tx-confirm--prod")).not.toBeNull();
    expect(document.body.textContent).toContain("解除「LUNAUSDT」的买入禁止");
    expect(document.body.textContent).toContain("这是生产环境，会使用真实资金。");
    expect(document.body.textContent).toContain("输入标的代码「LUNAUSDT」确认");
    expect(document.body.textContent).toContain("恢复正常买入");
    expect(document.body.textContent).toContain("下架清仓");
    expect(document.body.textContent).toContain("进行中的下架处理（含演练）会一并取消");

    // 未输入或输入错误代码时按钮禁用，不可提交
    expect(confirmButton("解除")?.disabled).toBe(true);
    await typeConfirmName("LUNA");
    expect(confirmButton("解除")?.disabled).toBe(true);
    confirmButton("解除")?.click();
    await flushPromises();
    expect(api.unblockInstrument).not.toHaveBeenCalled();

    // 输入完整标的代码后放行提交
    await typeConfirmName("LUNAUSDT");
    expect(confirmButton("解除")?.disabled).toBe(false);
    confirmButton("解除")?.click();
    await flushPromises();
    expect(api.unblockInstrument).toHaveBeenCalledWith({ exchange: "binance", market_type: "spot", exchange_symbol: "LUNAUSDT" });
    expect(wrapper.emitted("changed")).toHaveLength(1);
  });

  it("仅模拟盘（PAPER_ONLY）解除走模拟盘确认，无需输入标的代码即可提交", async () => {
    mountTab();
    await flushPromises();
    const buttons = wrapper.findAll("tbody button").filter(item => item.text() === "解除");
    await buttons[1].trigger("click");
    await flushPromises();
    expect(document.querySelector(".tx-confirm--paper")).not.toBeNull();
    expect(document.body.textContent).not.toContain("这是生产环境");
    expect(confirmInput()).toBeNull();
    expect(confirmButton("解除")?.disabled).toBe(false);

    confirmButton("解除")?.click();
    await flushPromises();
    expect(api.unblockInstrument).toHaveBeenCalledWith({
      exchange: "okx",
      market_type: "swap",
      exchange_symbol: "FTT-USDT-SWAP"
    });
    expect(wrapper.emitted("changed")).toHaveLength(1);
  });

  it("未知 scope 保守按生产级高危确认处理", async () => {
    mountTab([{ ...rows[0], scope: "" }]);
    await flushPromises();
    (wrapper.vm as unknown as TabVM).askUnblock({ ...rows[0], scope: "" });
    await flushPromises();
    expect(document.querySelector(".tx-confirm--prod")).not.toBeNull();
    expect(confirmButton("解除")?.disabled).toBe(true);
  });

  it("解除失败时错误显示在确认框里，不通知刷新", async () => {
    api.unblockInstrument.mockImplementation(async () => {
      throw new Error("交易服务暂时不可用");
    });
    mountTab();
    await flushPromises();
    (wrapper.vm as unknown as TabVM).askUnblock(rows[0]);
    await flushPromises();
    await typeConfirmName("LUNAUSDT");
    confirmButton("解除")?.click();
    await flushPromises();
    expect(document.body.textContent).toContain("交易服务暂时不可用");
    expect(wrapper.emitted("changed")).toBeUndefined();
  });
});

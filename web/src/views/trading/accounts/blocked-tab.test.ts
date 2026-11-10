import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BlockedInstrument } from "@/api/trade";

const api = vi.hoisted(() => ({ unblockInstrument: vi.fn() }));
vi.mock("@/api/trade", async () => ({ ...(await vi.importActual<typeof import("@/api/trade")>("@/api/trade")), ...api }));
import BlockedTab from "./blocked-tab.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const rows: BlockedInstrument[] = [
  {
    exchange: 1,
    market_type: 1,
    exchange_symbol: "LUNAUSDT",
    instrument_id: "LUNA-USDT-SPOT",
    base_asset: "LUNA",
    reason: "币安公告下架",
    source_event_id: "evt-1",
    blocked_at: "1700000000000"
  },
  {
    exchange: 2,
    market_type: 2,
    exchange_symbol: "FTT-USDT-SWAP",
    instrument_id: "",
    base_asset: "",
    reason: "",
    source_event_id: "",
    blocked_at: "1700000100000"
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

beforeEach(() => {
  api.unblockInstrument.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, removed: true });
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

describe("下架名单子页签", () => {
  it("列出交易所、市场、代码、原因和来源，缺的字段写“—”", async () => {
    mountTab();
    await flushPromises();
    const text = wrapper.text();
    expect(text).toContain("LUNAUSDT");
    expect(text).toContain("现货");
    expect(text).toContain("币安");
    expect(text).toContain("币安公告下架");
    expect(text).toContain("evt-1");
    expect(text).toContain("FTT-USDT-SWAP");
    expect(text).toContain("永续");
    expect(text).toContain("OKX");
    expect(wrapper.findAll("tbody tr")).toHaveLength(2);
  });

  it("名单为空时说明没有被禁止买入的标的", async () => {
    mountTab([]);
    await flushPromises();
    expect(wrapper.text()).toContain("没有被禁止买入的标的");
  });

  it("点“解除”先确认并说明后果，确认后按三元组解除并通知刷新", async () => {
    mountTab();
    await flushPromises();
    await wrapper
      .findAll("tbody button")
      .find(item => item.text() === "解除")!
      .trigger("click");
    await flushPromises();
    expect((wrapper.vm as unknown as TabVM).confirmVisible).toBe(true);
    expect(api.unblockInstrument).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("解除「LUNAUSDT」的买入禁止");
    expect(document.body.textContent).toContain("恢复正常买入");
    expect(document.body.textContent).toContain("下架清仓");
    confirmButton("解除")?.click();
    await flushPromises();
    expect(api.unblockInstrument).toHaveBeenCalledWith(
      { exchange: 1, market_type: 1, exchange_symbol: "LUNAUSDT" },
      { silent: true }
    );
    expect(wrapper.emitted("changed")).toHaveLength(1);
  });

  it("解除失败时错误显示在确认框里，不通知刷新", async () => {
    api.unblockInstrument.mockImplementation(async () => {
      throw new Error("交易服务暂时不可用");
    });
    mountTab();
    await flushPromises();
    (wrapper.vm as unknown as TabVM).askUnblock(rows[0]);
    await flushPromises();
    confirmButton("解除")?.click();
    await flushPromises();
    expect(document.body.textContent).toContain("交易服务暂时不可用");
    expect(wrapper.emitted("changed")).toBeUndefined();
  });
});

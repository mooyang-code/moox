import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import { computed, defineComponent, h, provide, reactive, ref } from "vue";
import type { LogicalAccount } from "@/api/trade";

const mocks = vi.hoisted(() => ({ listFills: vi.fn(), getOrder: vi.fn() }));
const nav = vi.hoisted(() => ({
  route: { query: {} as Record<string, unknown> },
  replace: vi.fn()
}));
vi.mock("vue-router", () => ({
  useRoute: () => nav.route,
  useRouter: () => ({ replace: nav.replace })
}));
vi.mock("@/api/trade", async () => ({
  ...(await vi.importActual<typeof import("@/api/trade")>("@/api/trade")),
  ...mocks
}));
import { detailContextKey, type DetailContext } from "./context";
import FillsTab from "./fills-tab.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

let wrapper: VueWrapper;

interface FillsVM {
  symbol: string;
  drawerOrder: { order_id: string } | null;
  search(): void;
  openOrderOf(fill: { order_id: string }): Promise<void>;
}

function mountTab(markets = [1]) {
  const logical = ref({
    logical_account_id: "l-1",
    markets,
    members: [],
    settlement_asset: "USDT"
  } as unknown as LogicalAccount);
  wrapper = mount(
    defineComponent({
      setup() {
        provide(detailContextKey, {
          logical: computed(() => logical.value),
          accounts: computed(() => new Map()),
          dataVersion: ref(0)
        } as unknown as DetailContext);
        return () => h(FillsTab);
      }
    }),
    { global: { plugins: [ArcoVue], stubs: { OrderDetailDrawer: true } } }
  );
  return wrapper.findComponent(FillsTab).vm as unknown as FillsVM;
}

describe("成交筛选写在地址里", () => {
  afterEach(() => wrapper?.unmount());

  it("进入页签时按地址里的筛选查询，永续市场读回成数字", async () => {
    mocks.listFills.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, fills: [] });
    nav.route = reactive({ query: { tab: "fills", frange: "30d", fsymbol: "BTC", fmarket: "2" } });
    mountTab([1, 2]);
    await flushPromises();
    const req = mocks.listFills.mock.calls[0][0];
    expect(req.instrument_id).toBe("BTC-USDT-SWAP");
    expect(req.start_time).toBeDefined();
  });

  it("查询时把非默认的筛选写回地址，默认值不写；输入框里没提交的币名不进查询", async () => {
    mocks.listFills.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, fills: [] });
    nav.replace.mockReset();
    nav.route = reactive({ query: { tab: "fills" } });
    const tab = mountTab();
    await flushPromises();
    tab.symbol = "BTC";
    expect(nav.replace).not.toHaveBeenCalledWith({ query: expect.objectContaining({ fsymbol: "BTC" }) });
    tab.search();
    expect(nav.replace).toHaveBeenLastCalledWith({
      query: expect.objectContaining({ tab: "fills", fsymbol: "BTC", frange: undefined, faccount: undefined })
    });
    await flushPromises();
    expect(mocks.listFills.mock.lastCall?.[0].instrument_id).toBe("BTC-USDT-SPOT");
  });
});

describe("点成交看所属订单", () => {
  afterEach(() => wrapper?.unmount());

  it("连续点两行时只认最后一次，先点的订单后到也不会盖掉", async () => {
    mocks.listFills.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, fills: [] });
    nav.route = reactive({ query: { tab: "fills" } });
    const tab = mountTab();
    await flushPromises();
    let resolveFirst: (value: unknown) => void = () => undefined;
    mocks.getOrder
      .mockReset()
      .mockImplementationOnce(() => new Promise(resolve => (resolveFirst = resolve)))
      .mockResolvedValueOnce({ order: { order_id: "o-2" } });
    const first = tab.openOrderOf({ order_id: "o-1" });
    await tab.openOrderOf({ order_id: "o-2" });
    expect(tab.drawerOrder?.order_id).toBe("o-2");
    resolveFirst({ order: { order_id: "o-1" } });
    await first;
    expect(tab.drawerOrder?.order_id).toBe("o-2");
  });
});

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import { computed, defineComponent, h, provide, reactive, ref } from "vue";
import type { LogicalAccount } from "@/api/trade";

const mocks = vi.hoisted(() => ({ listOrders: vi.fn(), getOrder: vi.fn() }));
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
import OrdersTab from "./orders-tab.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

let wrapper: VueWrapper;

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
          dataVersion: ref(0),
          openCancel: vi.fn()
        } as unknown as DetailContext);
        return () => h(OrdersTab);
      }
    }),
    { global: { plugins: [ArcoVue] } }
  );
  return wrapper.findComponent(OrdersTab).vm as unknown as {
    state: string | undefined;
    symbol: string;
    orders: unknown[];
    error: string;
    drawerOrder: { state: string } | null;
    openDrawer(order: unknown): void;
    search(): void;
  };
}

describe("订单筛选写在地址里", () => {
  afterEach(() => wrapper?.unmount());

  it("进入页签时按地址里的筛选查询", async () => {
    mocks.listOrders.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, orders: [] });
    nav.route = reactive({ query: { tab: "orders", orange: "30d", ostate: "FILLED", oowner: "SYSTEM" } });
    mountTab();
    await flushPromises();
    expect(mocks.listOrders.mock.calls[0][0]).toMatchObject({ state: "FILLED", owner_type: "SYSTEM" });
    expect(mocks.listOrders.mock.calls[0][0].start_time).toBeDefined();
  });

  it("地址里的市场读回成数字，永续不会被重置成现货", async () => {
    mocks.listOrders.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, orders: [] });
    nav.route = reactive({ query: { tab: "orders", osymbol: "BTC", omarket: "2" } });
    mountTab([1, 2]);
    await flushPromises();
    expect(mocks.listOrders.mock.calls[0][0].instrument_id).toBe("BTC-USDT-SWAP");
  });

  it("查询时把非默认的筛选写回地址，默认值不写", async () => {
    mocks.listOrders.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, orders: [] });
    nav.replace.mockReset();
    nav.route = reactive({ query: { tab: "orders" } });
    const tab = mountTab();
    await flushPromises();
    tab.state = "FILLED";
    tab.symbol = "BTC";
    tab.search();
    expect(nav.replace).toHaveBeenLastCalledWith({
      query: expect.objectContaining({ tab: "orders", ostate: "FILLED", osymbol: "BTC", orange: undefined })
    });
  });
});

describe("订单页的来源筛选", () => {
  afterEach(() => wrapper?.unmount());

  it("选了来源后带 owner_type 查询，清除后不带", async () => {
    mocks.listOrders.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, orders: [] });
    const logical = ref({
      logical_account_id: "l-1",
      markets: [1],
      members: [],
      settlement_asset: "USDT"
    } as unknown as LogicalAccount);
    wrapper = mount(
      defineComponent({
        setup() {
          provide(detailContextKey, {
            logical: computed(() => logical.value),
            accounts: computed(() => new Map()),
            dataVersion: ref(0),
            openCancel: vi.fn()
          } as unknown as DetailContext);
          return () => h(OrdersTab);
        }
      }),
      { global: { plugins: [ArcoVue] } }
    );
    await flushPromises();
    expect(mocks.listOrders.mock.calls[0][0].owner_type).toBeUndefined();

    const tab = wrapper.findComponent(OrdersTab).vm as unknown as { ownerType: string | undefined; search(): void };
    tab.ownerType = "SYSTEM";
    tab.search();
    await flushPromises();
    expect(mocks.listOrders).toHaveBeenLastCalledWith(
      expect.objectContaining({ logical_account_id: "l-1", owner_type: "SYSTEM", page: { page: 1, size: 20 } }),
      { silent: false }
    );

    tab.ownerType = "";
    tab.search();
    await flushPromises();
    expect(mocks.listOrders.mock.lastCall?.[0].owner_type).toBeUndefined();
  });
});

describe("未完成订单的后台刷新", () => {
  afterEach(() => {
    wrapper?.unmount();
    vi.useRealTimers();
  });

  it("有未完成订单时每 5 秒静默刷新，失败时保留列表、不显示错误", async () => {
    vi.useFakeTimers();
    const order = (state: string) => ({
      order_id: "o-1",
      state,
      trading_account_id: "t-1",
      instrument_id: "BTC-USDT-SPOT",
      quantity: "1",
      filled_quantity: "0",
      submitted_at: "1700000000000"
    });
    const open = order("OPEN");
    mocks.listOrders.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, orders: [open] });
    nav.route = reactive({ query: { tab: "orders" } });
    const tab = mountTab();
    await flushPromises();
    expect(mocks.listOrders).toHaveBeenCalledTimes(1);

    mocks.listOrders.mockRejectedValue(new Error("网络错误"));
    await vi.advanceTimersByTimeAsync(5000);
    expect(mocks.listOrders).toHaveBeenCalledTimes(2);
    expect(mocks.listOrders.mock.lastCall?.[1]).toEqual({ silent: true });
    expect(tab.orders).toHaveLength(1);
    expect(tab.error).toBe("");
  });

  it("输入框里没提交的币名不进后台刷新", async () => {
    vi.useFakeTimers();
    mocks.listOrders.mockReset().mockResolvedValue({
      ret_info: { code: 0, msg: "" },
      orders: [{ order_id: "o-1", state: "OPEN", instrument_id: "BTC-USDT-SPOT", quantity: "1", submitted_at: "1700000000000" }]
    });
    nav.route = reactive({ query: { tab: "orders" } });
    const tab = mountTab();
    await flushPromises();
    tab.symbol = "E";
    await vi.advanceTimersByTimeAsync(5000);
    expect(mocks.listOrders).toHaveBeenCalledTimes(2);
    expect(mocks.listOrders.mock.lastCall?.[0].instrument_id).toBeUndefined();
  });

  it("抽屉里的订单离开列表后单独读一次，不退回打开时的状态", async () => {
    vi.useFakeTimers();
    const row = { order_id: "o-1", state: "OPEN", instrument_id: "BTC-USDT-SPOT", quantity: "1", submitted_at: "1700000000000" };
    mocks.listOrders.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, orders: [row] });
    mocks.getOrder.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "" }, order: { ...row, state: "FILLED" } });
    nav.route = reactive({ query: { tab: "orders", ostate: "OPEN" } });
    const tab = mountTab();
    await flushPromises();
    tab.openDrawer(row);
    mocks.listOrders.mockResolvedValue({ ret_info: { code: 0, msg: "" }, orders: [] });
    await vi.advanceTimersByTimeAsync(5000);
    expect(mocks.getOrder).toHaveBeenCalledWith("o-1", { silent: true });
    expect(tab.drawerOrder?.state).toBe("FILLED");
  });

  it("没有未完成订单时不刷新", async () => {
    vi.useFakeTimers();
    mocks.listOrders.mockReset().mockResolvedValue({
      ret_info: { code: 0, msg: "" },
      orders: [{ order_id: "o-1", state: "FILLED", instrument_id: "BTC-USDT-SPOT", quantity: "1", submitted_at: "1700000000000" }]
    });
    nav.route = reactive({ query: { tab: "orders" } });
    mountTab();
    await flushPromises();
    await vi.advanceTimersByTimeAsync(10000);
    expect(mocks.listOrders).toHaveBeenCalledTimes(1);
  });
});

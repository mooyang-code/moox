import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import { computed, defineComponent, h, provide, reactive, ref } from "vue";
import type { LogicalAccount } from "@/api/trade";

const mocks = vi.hoisted(() => ({ listOrders: vi.fn() }));
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

function mountTab() {
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
  return wrapper.findComponent(OrdersTab).vm as unknown as { state: string | undefined; symbol: string; search(): void };
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

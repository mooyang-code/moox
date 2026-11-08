import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import { computed, defineComponent, h, provide, ref } from "vue";
import type { LogicalAccount } from "@/api/trade";

const mocks = vi.hoisted(() => ({ listOrders: vi.fn() }));
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
      expect.objectContaining({ logical_account_id: "l-1", owner_type: "SYSTEM", page: { page: 1, size: 20 } })
    );

    tab.ownerType = "";
    tab.search();
    await flushPromises();
    expect(mocks.listOrders.mock.lastCall?.[0].owner_type).toBeUndefined();
  });
});

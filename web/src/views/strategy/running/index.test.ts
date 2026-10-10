import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick, reactive } from "vue";

const api = vi.hoisted(() => ({
  loadInstances: vi.fn(),
  loadAllStrategies: vi.fn(),
  store: null as any,
  space: null as any
}));
vi.mock("vue-router", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => api.space }));
vi.mock("@/store/modules/strategy", () => ({ useStrategyStore: () => api.store }));

import RunningPage from "./index.vue";

const stubs = {
  "a-space": true,
  "a-button": true,
  "a-alert": true,
  "a-select": true,
  "a-option": true,
  "a-table": true,
  "a-table-column": true,
  "a-empty": true,
  "a-popconfirm": true,
  "icon-refresh": true,
  "icon-plus": true,
  StrategyInstanceCreate: true,
  StatusBadge: true
};

describe("strategy running page", () => {
  beforeEach(() => {
    api.loadInstances.mockReset().mockResolvedValue(undefined);
    api.space = reactive({ selectedSpaceId: "crypto" });
    api.store = reactive({
      strategyCatalog: [] as unknown[],
      strategiesComplete: false,
      instances: [],
      totalInstances: 0,
      listLoading: false,
      error: "",
      loadInstances: api.loadInstances,
      loadAllStrategies: vi.fn().mockImplementation(async () => {
        api.store.strategyCatalog = [{ strategy_id: "s1", name: "动量" }];
        api.store.strategiesComplete = true;
      })
    });
  });

  it("loads the full definition catalog once and skips it on later refreshes", async () => {
    const wrapper = mount(RunningPage, { global: { stubs } });
    await flushPromises();
    expect(api.store.loadAllStrategies).toHaveBeenCalledTimes(1);
    // 切换空间会触发再一次刷新实例列表；目录已完整，不应再读一遍。
    api.space.selectedSpaceId = "hk";
    await nextTick();
    await flushPromises();
    expect(api.loadInstances).toHaveBeenCalledTimes(2);
    expect(api.store.loadAllStrategies).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});

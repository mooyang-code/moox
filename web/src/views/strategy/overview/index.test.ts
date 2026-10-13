import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { reactive } from "vue";

const ctx = vi.hoisted(() => ({
  listInstances: vi.fn(),
  listReplays: vi.fn(),
  listStrategyTargets: vi.fn(),
  push: vi.fn(),
  store: null as any
}));
vi.mock("@/api/strategy", () => ({
  listInstances: ctx.listInstances,
  listReplays: ctx.listReplays,
  listStrategyTargets: ctx.listStrategyTargets
}));
vi.mock("vue-router", () => ({ useRouter: () => ({ push: ctx.push }) }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => ({ selectedSpaceId: "crypto" }) }));
vi.mock("@/store/modules/strategy", () => ({ useStrategyStore: () => ctx.store }));

import OverviewPage from "./index.vue";

function instance(id: string, overrides: Record<string, unknown> = {}) {
  return {
    instance_id: id,
    strategy_id: "s1",
    space_id: "crypto",
    view_id: "v",
    logical_account_id: "",
    enabled: true,
    session_id: "sess",
    resolved_json: "{}",
    health: "ok",
    created_at: "",
    updated_at: "2026-10-01T00:00:00Z",
    ...overrides
  };
}
const later = new Date(Date.now() + 3_600_000).toISOString();

const stubs = {
  "a-button": { template: "<button><slot /></button>" },
  "a-alert": { template: '<div class="alert"><slot /></div>' },
  "a-empty": { props: ["description"], template: '<div class="empty">{{ description }}</div>' },
  "a-tag": { template: '<span class="tag"><slot /></span>' },
  "a-link": { emits: ["click"], template: '<a class="link" @click="$emit(\'click\')"><slot /></a>' },
  "a-space": { template: "<span><slot /></span>" },
  "a-tooltip": { template: "<span><slot /></span>" },
  "icon-refresh": true,
  FactorInfoTip: true
};

describe("strategy overview page", () => {
  beforeEach(() => {
    ctx.store = reactive({
      strategyCatalog: [{ strategy_id: "s1", name: "动量" }],
      strategiesComplete: true,
      loadAllStrategies: vi.fn()
    });
    ctx.push.mockReset();
    ctx.listInstances.mockReset().mockResolvedValue({
      items: [instance("a"), instance("b"), instance("off", { enabled: false, session_id: "" })],
      page: { total: 3 }
    });
    ctx.listReplays.mockReset().mockResolvedValue({ items: [], page: { total: 0 } });
    ctx.listStrategyTargets.mockReset().mockImplementation(async (id: string) => {
      if (id === "b") throw new Error("读取失败");
      return {
        targets: [{ instrument_id: "BTC-USDT", target_weight: "0.25" }],
        session_id: "sess",
        bar_end_time: "2026-10-10T11:00:00Z",
        valid_until: later,
        result_id: "r"
      };
    });
  });

  it("reads targets only for enabled instances and survives a single failure", async () => {
    const wrapper = mount(OverviewPage, { global: { stubs } });
    await flushPromises();
    expect(ctx.listStrategyTargets.mock.calls.map(call => call[0]).sort()).toEqual(["a", "b"]);
    expect(wrapper.find(".alert").exists()).toBe(false);
    expect(wrapper.text()).toContain("BTC-USDT 25.00%");
    expect(wrapper.text()).toContain("已停用，不再产出目标");
  });

  it("links a card straight to that instance's results", async () => {
    const wrapper = mount(OverviewPage, { global: { stubs } });
    await flushPromises();
    await wrapper
      .findAll(".link")
      .find(item => item.text() === "结果")!
      .trigger("click");
    expect(ctx.push).toHaveBeenCalledWith({ name: "strategy-tasks", query: { tab: "results", instance: "a" } });
  });

  it("shows a load failure with a retry", async () => {
    ctx.listInstances.mockRejectedValueOnce(new Error("网络错误"));
    const wrapper = mount(OverviewPage, { global: { stubs } });
    await flushPromises();
    expect(wrapper.find(".alert").text()).toContain("总览读取失败：网络错误");
  });
});

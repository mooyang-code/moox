import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, reactive } from "vue";

const ctx = vi.hoisted(() => ({ store: null as any }));
vi.mock("vue-router", () => ({
  useRoute: () => ({ params: { instanceId: "i1" }, query: {} }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() })
}));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => ({ selectedSpaceId: "crypto" }) }));
vi.mock("@/store/modules/strategy", () => ({ useStrategyStore: () => ctx.store }));
vi.mock("@/views/strategy/use-now-clock", () => ({ useNowClock: () => ({ value: Date.now() }) }));
// 子组件换成只暴露分页属性与事件的替身：这里只验证详情页对结果翻页失败的处理。
vi.mock("@/views/strategy/components/strategy-result-table.vue", () => ({
  default: defineComponent({
    props: ["page", "scope", "loading", "error"],
    emits: ["page", "retry"],
    setup(props, { emit }) {
      return () =>
        h("div", { class: "results" }, [
          h("span", { class: "state" }, `page=${props.page};scope=${props.scope};error=${props.error}`),
          h("button", { class: "next", onClick: () => emit("page", 2, "all") }),
          h("button", { class: "retry", onClick: () => emit("retry") })
        ]);
    }
  })
}));
import DetailPage from "./index.vue";

const stubs = {
  "a-button": { template: "<button><slot /></button>" },
  "a-switch": true,
  "a-space": { template: "<div><slot /></div>" },
  "a-alert": { template: '<div class="alert"><slot /></div>' },
  "a-descriptions": { template: "<div><slot /></div>" },
  "a-descriptions-item": { template: "<div><slot /></div>" },
  "a-tabs": { template: "<div><slot /></div>" },
  "a-tab-pane": { template: "<div><slot /></div>" },
  "a-link": true,
  "a-tag": true,
  "a-popconfirm": true,
  "a-table": true,
  "a-table-column": true,
  "a-tooltip": { template: "<span><slot /></span>" },
  "a-empty": true,
  "icon-left": true,
  "icon-refresh": true
};

describe("strategy detail page result paging", () => {
  beforeEach(() => {
    ctx.store = reactive({
      instance: {
        instance_id: "i1",
        space_id: "crypto",
        enabled: false,
        session_id: "s1",
        strategy_id: "s",
        view_id: "v",
        logical_account_id: "",
        health: "ok",
        resolved_json: "{}"
      },
      strategy: null,
      strategyStatus: "loaded",
      targetSnapshot: null,
      results: [],
      totalResults: 0,
      detailLoading: false,
      detailError: "",
      resultsLoading: false,
      resultsError: "",
      loadedResultPage: 1,
      loadedResultAllHistory: false,
      loadInstanceDetail: vi.fn().mockResolvedValue(true),
      loadResultPage: vi.fn().mockResolvedValue(undefined),
      startPolling: vi.fn(),
      stopPolling: vi.fn(),
      clearDetail: vi.fn()
    });
  });

  it("moves to the requested page and scope when the read succeeds", async () => {
    const wrapper = mount(DetailPage, { global: { stubs } });
    await flushPromises();
    await wrapper.find(".next").trigger("click");
    await flushPromises();
    expect(ctx.store.loadResultPage).toHaveBeenCalledWith(2, 20, true);
    expect(wrapper.find(".state").text()).toBe("page=2;scope=all;error=");
  });

  it("returns the pager to the last loaded page and scope when the read fails", async () => {
    const wrapper = mount(DetailPage, { global: { stubs } });
    await flushPromises();
    ctx.store.loadResultPage.mockImplementationOnce(async () => {
      ctx.store.resultsError = "翻页失败";
      throw new Error("翻页失败");
    });
    await wrapper.find(".next").trigger("click");
    await flushPromises();
    expect(wrapper.find(".state").text()).toBe("page=1;scope=session;error=翻页失败");
  });

  it("retries the current page and scope", async () => {
    const wrapper = mount(DetailPage, { global: { stubs } });
    await flushPromises();
    await wrapper.find(".retry").trigger("click");
    await flushPromises();
    expect(ctx.store.loadResultPage).toHaveBeenLastCalledWith(1, 20, false);
  });

  it("does not treat a result read error as an instance detail failure", async () => {
    ctx.store.resultsError = "结果失败";
    const wrapper = mount(DetailPage, { global: { stubs } });
    await flushPromises();
    expect(wrapper.text()).not.toContain("详情加载失败");
  });
});

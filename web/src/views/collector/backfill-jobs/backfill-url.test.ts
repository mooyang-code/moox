import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { defineComponent, h, KeepAlive, reactive, ref } from "vue";
import { describe, expect, it, vi } from "vitest";

const ctx = vi.hoisted(() => ({
  route: null as unknown as { query: Record<string, string> },
  replace: vi.fn()
}));
const api = vi.hoisted(() => ({
  listKlineBackfills: vi.fn(async () => ({ items: [], page: { total: 0 } })),
  getCollectorStatus: vi.fn(async () => ({ datasets: [] }))
}));
vi.mock("vue-router", () => ({ useRoute: () => ctx.route, useRouter: () => ({ replace: ctx.replace }) }));
vi.mock("@/api/collector/runtime-status", async importOriginal => ({
  ...(await importOriginal<typeof import("@/api/collector/runtime-status")>()),
  listKlineBackfills: api.listKlineBackfills,
  getCollectorStatus: api.getCollectorStatus
}));
vi.mock("@/store/modules/space", async () => {
  const { defineStore } = await import("pinia");
  const { ref } = await import("vue");
  return {
    useSpaceStore: defineStore("space-test", () => ({ selectedSpaceId: ref("crypto"), selectedSpace: ref({ timezone: "UTC" }) }))
  };
});
import { useSpaceStore } from "@/store/modules/space";
import BackfillJobs from "./index.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

// 和真实的页签容器一样放在 keep-alive 里；show 置 false 等于切到别的页签。
const show = ref(true);
const Other = defineComponent({ setup: () => () => h("div") });
const Host = defineComponent({ setup: () => () => h(KeepAlive, null, [show.value ? h(BackfillJobs) : h(Other)]) });

describe("补采页的 URL 状态", () => {
  it("URL 带 segment、dataset 时按它筛选，URL 变了跟着变；切空间后筛选回到默认", async () => {
    ctx.route = reactive({ query: { tab: "backfills", segment: "manual", dataset: "ds_1m" } });
    ctx.replace.mockReset();
    const wrapper = mount(Host, { global: { plugins: [ArcoVue], stubs: { KlineRangeDialog: true } } });
    await flushPromises();
    expect(api.listKlineBackfills).toHaveBeenLastCalledWith(
      "crypto",
      { dataset_id: "ds_1m", source: "manual" },
      1,
      20,
      expect.anything()
    );

    ctx.route.query = { tab: "backfills", segment: "auto" };
    await flushPromises();
    expect(api.listKlineBackfills).toHaveBeenLastCalledWith("crypto", { source: "auto" }, 1, 20, expect.anything());

    // 切走期间 URL 换了筛选：失活时不跟随，切回来按 URL 重读
    show.value = false;
    await flushPromises();
    api.listKlineBackfills.mockClear();
    ctx.route.query = { tab: "backfills", segment: "manual" };
    await flushPromises();
    expect(api.listKlineBackfills).not.toHaveBeenCalled();
    show.value = true;
    await flushPromises();
    expect(api.listKlineBackfills).toHaveBeenLastCalledWith("crypto", { source: "manual" }, 1, 20, expect.anything());

    useSpaceStore().selectedSpaceId = "stockcn";
    await flushPromises();
    expect(api.listKlineBackfills).toHaveBeenLastCalledWith("stockcn", { attention: true }, 1, 20, expect.anything());
    wrapper.unmount();
  });
});

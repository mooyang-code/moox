import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { reactive } from "vue";
import { describe, expect, it, vi } from "vitest";

const ctx = vi.hoisted(() => ({
  route: null as unknown as { query: Record<string, string> },
  replace: vi.fn(),
  space: null as unknown as { selectedSpaceId: string }
}));
vi.mock("vue-router", () => ({ useRoute: () => ctx.route, useRouter: () => ({ replace: ctx.replace }) }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => ctx.space }));
// 页签内容和这里无关，换成空组件
const stub = vi.hoisted(() => (name: string) => ({ default: { name, render: () => name } }));
vi.mock("@/views/collector/collection-tasks/collection-tasks.vue", () => stub("tasks"));
vi.mock("@/views/collector/runtime-status/runtime-status.vue", () => stub("status"));
vi.mock("@/views/collector/backfill-jobs/index.vue", () => stub("backfills"));
vi.mock("@/views/collector/cloud-function/function-instance.vue", () => stub("executors"));
vi.mock("@/views/collector/task-results/index.vue", () => stub("results"));
import TaskManagement from "./index.vue";

function setup(query: Record<string, string>) {
  ctx.route = reactive({ query });
  ctx.space = reactive({ selectedSpaceId: "crypto" });
  ctx.replace.mockReset().mockImplementation(({ query: next }: { query: Record<string, string> }) => {
    ctx.route.query = next;
  });
  return mount(TaskManagement, { global: { plugins: [ArcoVue] } });
}

describe("采集任务的页签容器", () => {
  it("切页签只改 tab，别的页签的参数原样保留", async () => {
    const wrapper = setup({ keyword: "btc", page: "2", detail: "t1" });
    (wrapper.vm as unknown as { onTabChange(value: string): void }).onTabChange("results");
    expect(ctx.route.query).toEqual({ keyword: "btc", page: "2", detail: "t1", tab: "results" });
    (wrapper.vm as unknown as { onTabChange(value: string): void }).onTabChange("tasks");
    expect(ctx.route.query).toEqual({ keyword: "btc", page: "2", detail: "t1" });
  });

  it("切空间时清掉各页签属于旧空间的参数，只保留当前页签", async () => {
    setup({ tab: "results", resultTask: "r1", keyword: "btc", job: "j1" });
    ctx.space.selectedSpaceId = "stockcn";
    await flushPromises();
    expect(ctx.route.query).toEqual({ tab: "results" });
  });
});

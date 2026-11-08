import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { reactive } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const ctx = vi.hoisted(() => ({
  route: null as unknown as { path: string; query: Record<string, string> },
  replace: vi.fn()
}));
const api = vi.hoisted(() => ({
  GetTaskList: vi.fn(),
  GetTaskDetail: vi.fn(),
  GetDataTypeConfigs: vi.fn(async () => ({ configs: [] }))
}));
vi.mock("vue-router", () => ({ useRoute: () => ctx.route, useRouter: () => ({ replace: ctx.replace, push: ctx.replace }) }));
vi.mock("@/api/collector", async importOriginal => ({
  ...(await importOriginal<typeof import("@/api/collector")>()),
  GetTaskList: api.GetTaskList,
  GetTaskDetail: api.GetTaskDetail,
  GetDataTypeConfigs: api.GetDataTypeConfigs
}));
vi.mock("@/api/storage/metadata", () => ({
  getDataset: vi.fn(),
  getView: vi.fn(),
  listDataNodes: vi.fn(async () => ({ data_nodes: [], page_result: {} })),
  listFieldGroups: vi.fn(async () => ({ field_groups: [], page_result: {} })),
  listFields: vi.fn(async () => ({ fields: [], page_result: {} })),
  listTags: vi.fn(async () => ({ tags: [], page_result: {} }))
}));
vi.mock("@/store/modules/space", async () => {
  const { defineStore } = await import("pinia");
  const { ref } = await import("vue");
  return {
    useSpaceStore: defineStore("space-test", () => ({ selectedSpaceId: ref("crypto"), selectedSpace: ref({ timezone: "UTC" }) }))
  };
});
vi.mock("@/store/modules/user-info", async () => {
  const { defineStore } = await import("pinia");
  const { ref } = await import("vue");
  return { useUserInfoStore: defineStore("user-info-test", () => ({ account: ref({ user: { username: "e2e" } }) })) };
});
import CollectionTasks from "./collection-tasks.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const task = (id: string) => ({ task_id: id, task_name: id, space_id: "crypto", data_type: "kline", enabled: true });

let wrapper: VueWrapper;
beforeEach(() => {
  ctx.route = reactive({ path: "/collector/tasks", query: {} });
  ctx.replace.mockReset().mockImplementation(({ query }: { query: Record<string, string> }) => {
    ctx.route.query = Object.fromEntries(Object.entries(query).filter(([, value]) => value !== undefined)) as Record<
      string,
      string
    >;
  });
  api.GetTaskList.mockReset().mockResolvedValue({ tasks: [task("t1")], page: { total: 1 } });
  api.GetTaskDetail.mockReset().mockResolvedValue({ task: task("t9") });
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

async function mountPage() {
  wrapper = mount(CollectionTasks, {
    global: {
      plugins: [ArcoVue],
      stubs: { ResampleJobsDrawer: true, ArchiveCoverageText: true, "icon-plus": true, "icon-search": true, "icon-more": true }
    },
    attachTo: document.body
  });
  await flushPromises();
  return wrapper;
}

describe("采集任务页的 URL 状态", () => {
  it("改 URL 里的筛选和页码，列表条件跟着变（浏览器前进后退）", async () => {
    await mountPage();
    api.GetTaskList.mockClear();
    ctx.route.query = { keyword: "btc", page: "2" };
    await flushPromises();
    expect(api.GetTaskList).toHaveBeenCalledWith(
      expect.objectContaining({ keyword: "btc", page: expect.objectContaining({ page: 2 }) })
    );
  });

  it("URL 带 detail 时打开详情；目标不在当前页时单独取；关掉抽屉后 URL 去掉 detail，别的参数保留", async () => {
    ctx.route.query = { detail: "t9", resultTask: "r1" };
    await mountPage();
    expect(api.GetTaskDetail).toHaveBeenCalledWith({ space_id: "crypto", task_id: "t9" });
    expect(document.querySelector(".arco-drawer")?.textContent).toContain("任务详情");
    (document.querySelector(".arco-drawer-close-btn") as HTMLElement).click();
    await flushPromises();
    expect(ctx.route.query.detail).toBeUndefined();
    expect(ctx.route.query.resultTask).toBe("r1");
  });

  it("在当前页的任务直接打开，不再单独取", async () => {
    ctx.route.query = { detail: "t1" };
    await mountPage();
    expect(api.GetTaskDetail).not.toHaveBeenCalled();
    expect(document.querySelector(".arco-drawer")?.textContent).toContain("任务详情");
  });
});

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { reactive } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ArchiveTask } from "@/api/archive";

const ctx = vi.hoisted(() => ({
  route: null as unknown as { query: Record<string, string> },
  replace: vi.fn(),
  warning: vi.fn()
}));
const api = vi.hoisted(() => ({
  getArchiveOverview: vi.fn(),
  listArchiveTasks: vi.fn(),
  listDatasets: vi.fn(),
  deleteArchiveTask: vi.fn(async () => ({})),
  invalidateArchiveCoverage: vi.fn()
}));
vi.mock("vue-router", () => ({
  useRoute: () => ctx.route,
  useRouter: () => ({ replace: ctx.replace })
}));
vi.mock("@/api/archive", async importOriginal => ({
  ...(await importOriginal<typeof import("@/api/archive")>()),
  getArchiveOverview: api.getArchiveOverview,
  listArchiveTasks: api.listArchiveTasks,
  deleteArchiveTask: api.deleteArchiveTask
}));
vi.mock("./archive-coverage", async importOriginal => ({
  ...(await importOriginal<typeof import("./archive-coverage")>()),
  invalidateArchiveCoverage: api.invalidateArchiveCoverage
}));
vi.mock("@/api/storage/metadata", () => ({ listDatasets: api.listDatasets }));
vi.mock("@/store/modules/space", () => ({
  useSpaceStore: () => ({ selectedSpaceId: "crypto", selectedSpace: { timezone: "UTC" } })
}));
vi.mock("@arco-design/web-vue", async importOriginal => {
  const actual = await importOriginal<typeof import("@arco-design/web-vue")>();
  return { ...actual, Message: { ...actual.Message, warning: ctx.warning, success: vi.fn() } };
});
import ArchivePage from "./index.vue";
import TaskEditorDrawer from "./task-editor-drawer.vue";
import TaskTable from "./task-table.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const task = (over: Partial<ArchiveTask> = {}): ArchiveTask => ({
  dataset_id: "ds_1m",
  freq: "1m",
  retention: "840h",
  pull_every_bars: 1,
  status: "enabled",
  next_pull_at: "2026-11-07T04:41:25Z",
  health: "warning",
  health_reason: "最近一次复核失败：Storage 超时",
  partition_count: 3,
  ...over
});

let wrapper: VueWrapper;
beforeEach(() => {
  ctx.route = reactive({ query: {} });
  ctx.replace.mockReset().mockImplementation(({ query }: { query: Record<string, string> }) => {
    ctx.route.query = query;
  });
  ctx.warning.mockReset();
  api.getArchiveOverview.mockReset().mockResolvedValue({ worker: { worker_id: "archive-worker@factor", active: true } });
  api.listArchiveTasks.mockReset().mockResolvedValue({ tasks: [task()] });
  api.listDatasets.mockReset().mockResolvedValue({ datasets: [], page_result: {} });
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

async function mountPage() {
  wrapper = mount(ArchivePage, { global: { plugins: [ArcoVue], stubs: { "icon-plus": true } }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

describe("数据归档页", () => {
  it("首次读取失败：显示失败和重试，不出现“正常”，也不显示空表", async () => {
    api.getArchiveOverview.mockRejectedValue(new Error("connect ECONNREFUSED"));
    const page = await mountPage();
    const text = page.text();
    expect(text).toContain("无法读取归档服务：connect ECONNREFUSED");
    expect(text).toContain("archive-mgr 是可选组件");
    expect(text).toContain("无法读取归档任务");
    expect(text).not.toContain("正常");
    expect(text).not.toContain("这个空间没有需要归档的时序数据集");
    // 两路读取任一失败，未归档提醒也写明暂不可用
    expect(text).toContain("未归档提醒暂不可用");
  });

  it("后台读取都带 silentError", async () => {
    await mountPage();
    expect(api.getArchiveOverview).toHaveBeenCalledWith({ silentError: true });
    expect(api.listArchiveTasks).toHaveBeenCalledWith({ page: 1, size: 100 }, { silentError: true });
    expect(api.listDatasets.mock.calls[0][1]).toEqual({ silentError: true });
  });

  it("健康原因直接写在单元格里", async () => {
    const page = await mountPage();
    expect(page.find(".reason").text()).toBe("最近一次复核失败：Storage 超时");
  });

  it("?task= 指向不存在的任务时不打开抽屉，提示并清掉参数", async () => {
    ctx.route.query = { task: "gone_1m" };
    await mountPage();
    expect(ctx.warning).toHaveBeenCalledWith("任务不存在或不在当前空间");
    expect(ctx.route.query).toEqual({});
    expect(document.querySelector(".arco-drawer")).toBeNull();
  });

  it("建好任务后清掉采集任务页的归档状态缓存，重读任务表再打开详情", async () => {
    const page = await mountPage();
    api.invalidateArchiveCoverage.mockClear();
    api.listArchiveTasks.mockResolvedValue({ tasks: [task(), task({ dataset_id: "ds_5m", partition_count: 0 })] });
    page.findComponent(TaskEditorDrawer).vm.$emit("created", "ds_5m");
    await flushPromises();
    expect(api.invalidateArchiveCoverage).toHaveBeenCalledTimes(1);
    expect(ctx.route.query).toEqual({ task: "ds_5m" });
    expect(ctx.warning).not.toHaveBeenCalled();
  });

  it("删除任务确认后清掉采集任务页的归档状态缓存", async () => {
    api.listArchiveTasks.mockResolvedValue({ tasks: [task({ partition_count: 0 })] });
    const page = await mountPage();
    api.invalidateArchiveCoverage.mockClear();
    await page
      .findAll("button")
      .find(item => item.text() === "更多")!
      .trigger("click");
    await flushPromises();
    (
      [...document.querySelectorAll(".arco-dropdown-option")].find(item => item.textContent?.includes("删除")) as HTMLElement
    ).click();
    await flushPromises();
    expect(document.body.textContent).toContain("删除后这个数据集不再归档");
    (document.querySelector(".arco-modal-footer .arco-btn-status-danger") as HTMLElement).click();
    await flushPromises();
    expect(api.deleteArchiveTask).toHaveBeenCalledWith("ds_1m");
    expect(api.invalidateArchiveCoverage).toHaveBeenCalledTimes(1);
  });

  it("?task= 指向存在的任务时打开详情", async () => {
    ctx.route.query = { task: "ds_1m" };
    await mountPage();
    expect(document.querySelector(".arco-drawer")?.textContent).toContain("ds_1m");
  });
});

describe("任务表", () => {
  const props = {
    loaded: true,
    error: "",
    unarchivedCount: 0,
    datasetsLoaded: true,
    names: {},
    zone: "UTC",
    nowMs: Date.parse("2026-11-07T04:40:00Z"),
    busy: {}
  };

  it("已有分区的任务删除置灰并说明原因，没有分区的可以删", async () => {
    wrapper = mount(TaskTable, {
      props: { ...props, tasks: [task(), task({ dataset_id: "empty_1m", partition_count: 0 })] },
      global: { plugins: [ArcoVue] },
      attachTo: document.body
    });
    await flushPromises();
    const moreButtons = wrapper.findAll("button").filter(item => item.text() === "更多");
    await moreButtons[0].trigger("click");
    await flushPromises();
    const removeOption = [...document.querySelectorAll(".arco-dropdown-option")].find(item => item.textContent?.includes("删除"));
    expect(removeOption?.className).toContain("arco-dropdown-option-disabled");
    expect(removeOption?.getAttribute("title")).toBe("已有分区的任务不能删除，只能停用");
    (removeOption as HTMLElement).click();
    expect(wrapper.emitted("remove")).toBeUndefined();
  });

  it("下次拉取到分钟、悬停看到秒，都带后缀", async () => {
    wrapper = mount(TaskTable, { props: { ...props, tasks: [task()] }, global: { plugins: [ArcoVue] } });
    await flushPromises();
    expect(wrapper.text()).toContain("每收盘 1 根 · 下次 2026-11-07 04:41 UTC");
    expect(wrapper.find('[title="下次拉取 2026-11-07 04:41:25 UTC"]').exists()).toBe(true);
  });
});

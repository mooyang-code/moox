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
  listArchiveRuns: vi.fn(async () => ({ runs: [], page_result: { total: 0 } })),
  listDatasets: vi.fn(),
  deleteArchiveTask: vi.fn(async () => ({})),
  clearArchiveUnrecoverable: vi.fn(async () => ({ removed: [] })),
  invalidateArchiveCoverage: vi.fn(),
  listDeployments: vi.fn(),
  listAllDeploymentHosts: vi.fn()
}));
vi.mock("vue-router", () => ({
  useRoute: () => ctx.route,
  useRouter: () => ({ replace: ctx.replace })
}));
vi.mock("@/api/archive", async importOriginal => ({
  ...(await importOriginal<typeof import("@/api/archive")>()),
  getArchiveOverview: api.getArchiveOverview,
  listArchiveTasks: api.listArchiveTasks,
  listArchiveRuns: api.listArchiveRuns,
  deleteArchiveTask: api.deleteArchiveTask,
  clearArchiveUnrecoverable: api.clearArchiveUnrecoverable
}));
vi.mock("../shared/archive-coverage", async importOriginal => ({
  ...(await importOriginal<typeof import("../shared/archive-coverage")>()),
  invalidateArchiveCoverage: api.invalidateArchiveCoverage
}));
vi.mock("@/api/storage/metadata", () => ({ listDatasets: api.listDatasets }));
vi.mock("@/api/admin/sysdeploy", () => ({
  listDeployments: api.listDeployments,
  listAllDeploymentHosts: api.listAllDeploymentHosts
}));
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
  status: "enabled",
  last_success_at: String(Date.parse("2026-11-07T04:41:25Z")),
  health: "warning",
  health_reason: "最近一次运行失败：Storage 超时",
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
  api.listDeployments
    .mockReset()
    .mockResolvedValue({ deployments: [{ host_id: "factor", component_id: "archive-worker", status: "enabled" }] });
  api.listAllDeploymentHosts
    .mockReset()
    .mockResolvedValue([{ host_id: "factor", label: "因子机", address: "192.168.0.103", status: "enabled" }]);
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

async function mountPage() {
  wrapper = mount(ArchivePage, {
    global: {
      plugins: [ArcoVue],
      stubs: { "icon-plus": true, "router-link": { props: ["to"], template: '<a :href="to"><slot /></a>' } }
    },
    attachTo: document.body
  });
  await flushPromises();
  return wrapper;
}

describe("归档任务页", () => {
  it("顶部一行：在线与否、所在主机、磁盘不足，链到归档存储", async () => {
    api.getArchiveOverview.mockResolvedValue({
      worker: {
        worker_id: "archive-worker@factor",
        active: true,
        disk_total_bytes: "100",
        disk_free_bytes: "1",
        min_free_bytes: "10"
      }
    });
    const page = await mountPage();
    const line = page.find(".worker-line").text();
    expect(line).toContain("归档执行器 在线");
    expect(line).toContain("所在主机 因子机（192.168.0.103）");
    expect(line).toContain("磁盘空间不足");
    expect(page.find('.worker-line a[href="/archive/storage"]').exists()).toBe(true);
  });

  it("首次读取失败：显示失败和重试，不出现“正常”，也不显示空表", async () => {
    api.getArchiveOverview.mockRejectedValue(new Error("connect ECONNREFUSED"));
    const page = await mountPage();
    const text = page.text();
    expect(text).toContain("无法读取归档服务：connect ECONNREFUSED");
    expect(text).toContain("archive-mgr 是可选组件");
    expect(text).toContain("无法读取归档任务");
    expect(text).not.toContain("正常");
    expect(text).not.toContain("还没有归档任务");
  });

  it("打开页面读执行器状态与任务列表", async () => {
    await mountPage();
    expect(api.getArchiveOverview).toHaveBeenCalledWith();
    expect(api.listArchiveTasks).toHaveBeenCalledWith({ page: 1, size: 100 });
  });

  it("健康原因直接写在单元格里", async () => {
    const page = await mountPage();
    expect(page.find(".reason").text()).toBe("最近一次运行失败：Storage 超时");
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

  it("?task= 指向存在的任务时打开详情，并经 ClearArchiveUnrecoverable 清空无法补齐区间", async () => {
    api.listArchiveTasks.mockResolvedValue({
      tasks: [
        task({
          unrecoverable: [{ start_time: "2026-10-01T00:00:00Z", end_time: "2026-10-02T00:00:00Z" }]
        })
      ]
    });
    api.clearArchiveUnrecoverable.mockClear();
    ctx.route.query = { task: "ds_1m" };
    await mountPage();
    expect(document.querySelector(".arco-drawer")?.textContent).toContain("ds_1m");
    const clearBtn = [...document.querySelectorAll<HTMLElement>(".arco-drawer button")].find(
      item => item.textContent?.trim() === "清除这些记录"
    )!;
    clearBtn.click();
    await flushPromises();
    (document.querySelector(".arco-modal-footer .arco-btn-status-danger") as HTMLElement).click();
    await flushPromises();
    expect(api.clearArchiveUnrecoverable).toHaveBeenCalledWith({ dataset_id: "ds_1m" });
  });
});

describe("任务表", () => {
  const props = {
    loaded: true,
    error: "",
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

  it("跟随列写每分钟跟随、最近成功、整段重读进度与最近失败原因", async () => {
    wrapper = mount(TaskTable, {
      props: {
        ...props,
        tasks: [
          task({
            rescan_from: "2026-11-01T00:00:00Z",
            rescan_until: "2026-11-07T04:00:00Z",
            last_follow_error: "storage timeout"
          })
        ]
      },
      global: { plugins: [ArcoVue] }
    });
    await flushPromises();
    expect(wrapper.text()).toContain("每分钟跟随变更日志");
    expect(wrapper.text()).toContain("最近成功 2026-11-07 04:41 UTC");
    expect(wrapper.text()).toContain("整段重读：已读到 2026-11-01 00:00 UTC，截止 2026-11-07 04:00 UTC");
    expect(wrapper.text()).toContain("最近失败：storage timeout");
    expect(wrapper.find('[title="2026-11-07 04:41:25 UTC"]').exists()).toBe(true);
    expect(wrapper.text()).toContain("整段重读");
  });
});

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ArchiveTask } from "@/api/archive";

const api = vi.hoisted(() => ({
  prepareArchiveTask: vi.fn(),
  previewArchiveSchedule: vi.fn(),
  createArchiveTask: vi.fn(async () => ({})),
  updateArchiveTask: vi.fn(async () => ({}))
}));
vi.mock("@/api/archive", () => api);
import TaskEditorDrawer from "./task-editor-drawer.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const EARLIEST = "2026-10-04T00:00:00Z";
const task = (over: Partial<ArchiveTask> = {}): ArchiveTask => ({
  dataset_id: "ds_1m",
  freq: "1m",
  retention: "840h",
  pull_every_bars: 1,
  status: "enabled",
  // 建任务时的最早值，比现在的最早值早：没改就不能再拿现在的最早值校验
  backfill_from: "2026-10-01T00:00:00Z",
  last_success_at: "",
  ...over
});

let wrapper: VueWrapper;
beforeEach(() => {
  api.prepareArchiveTask
    .mockReset()
    .mockResolvedValue({ freq: "1m", earliest_backfill_from: EARLIEST, recommended_pull_every_bars: 1 });
  api.previewArchiveSchedule.mockReset().mockResolvedValue({ next_runs: [], next_reviews: [] });
  api.updateArchiveTask.mockClear();
});
afterEach(() => {
  vi.useRealTimers();
  wrapper?.unmount();
  document.body.innerHTML = "";
});

function mountEditor(props: Record<string, unknown>) {
  wrapper = mount(TaskEditorDrawer, {
    props: { visible: true, mode: "edit", task: task(), presetDatasetId: "", datasets: [], tasks: [], zone: "UTC", ...props },
    global: { plugins: [ArcoVue] },
    attachTo: document.body
  });
  return wrapper;
}

const okButton = () => document.querySelector<HTMLElement>(".arco-drawer-footer .arco-btn-primary")!;

describe("编辑归档任务", () => {
  it("还没有成功的复核时显示回补起点；已有成功的复核时改成“请用补拉区间”的提示", async () => {
    mountEditor({ task: task() });
    await flushPromises();
    expect(document.body.textContent).toContain("回补起点");
    wrapper.unmount();
    document.body.innerHTML = "";
    mountEditor({ task: task({ last_success_at: "2026-11-07T00:34:00Z" }) });
    await flushPromises();
    expect(document.body.textContent).toContain("首次复核已完成，回补起点不再使用");
  });

  it("回补起点没改时不校验（原值早于现在的最早值也能存），只改拉取间隔时提交空的回补起点", async () => {
    mountEditor({ task: task() });
    await flushPromises();
    expect(document.body.textContent).not.toContain("不能早于");
    (wrapper.vm as unknown as { form: { pullEveryBars: number } }).form.pullEveryBars = 5;
    okButton().click();
    await flushPromises();
    expect(api.updateArchiveTask).toHaveBeenCalledWith({ dataset_id: "ds_1m", pull_every_bars: 5, backfill_from: "" });
  });
});

describe("拉取预览", () => {
  it("间隔变化后防抖 400ms 再预览；过期的结果丢弃", async () => {
    vi.useFakeTimers();
    let resolveFirst: (value: unknown) => void = () => undefined;
    api.previewArchiveSchedule
      .mockImplementationOnce(() => new Promise(resolve => (resolveFirst = resolve)))
      .mockResolvedValueOnce({ next_runs: ["2026-11-07T00:05:25Z"], next_reviews: [] });
    // 和页面一样：抽屉先关着，打开时才开始预览
    mountEditor({ task: task(), visible: false });
    await wrapper.setProps({ visible: true });
    await vi.advanceTimersByTimeAsync(400);
    expect(api.previewArchiveSchedule).toHaveBeenCalledTimes(1);

    const vm = wrapper.vm as unknown as { form: { pullEveryBars: number } };
    vm.form.pullEveryBars = 2;
    await vi.advanceTimersByTimeAsync(100);
    vm.form.pullEveryBars = 3;
    await vi.advanceTimersByTimeAsync(399);
    expect(api.previewArchiveSchedule).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(api.previewArchiveSchedule).toHaveBeenCalledTimes(2);
    expect(api.previewArchiveSchedule).toHaveBeenLastCalledWith(
      { dataset_id: "ds_1m", pull_every_bars: 3 },
      { silentError: true }
    );

    // 第一次的结果晚到：丢弃，不覆盖第二次的
    resolveFirst({ next_runs: ["2030-01-01T00:00:00Z"], next_reviews: [] });
    await flushPromises();
    expect(document.body.textContent).toContain("2026-11-07 00:05:25 UTC");
    expect(document.body.textContent).not.toContain("2030-01-01");
  });

  it("分区预计导出按后端的文本解析，时刻按空间时区显示", async () => {
    api.previewArchiveSchedule.mockResolvedValue({
      next_runs: [],
      next_reviews: [],
      partition_export_estimates: ["分区 2026-11-06：预计 2026-11-07T00:15:25Z 导出"]
    });
    mountEditor({ task: task(), visible: false, zone: "Asia/Shanghai" });
    await wrapper.setProps({ visible: true });
    await new Promise(resolve => setTimeout(resolve, 450));
    await flushPromises();
    expect(document.body.textContent).toContain("分区 2026-11-06 预计 2026-11-07 08:15:25 UTC+8 导出");
  });
});

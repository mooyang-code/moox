import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ArchiveTask } from "@/api/archive";

const api = vi.hoisted(() => ({
  prepareArchiveTask: vi.fn(),
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
  status: "enabled",
  backfill_from: "2026-10-01T00:00:00Z",
  last_success_at: "",
  ...over
});

let wrapper: VueWrapper;
beforeEach(() => {
  api.prepareArchiveTask.mockReset().mockResolvedValue({ freq: "1m", retention: "840h", earliest_backfill_from: EARLIEST });
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
  it("回补起点只读，只能启用、停用；状态没变时不提交", async () => {
    mountEditor({ task: task() });
    await flushPromises();
    expect(document.body.textContent).toContain("建好后不能改");
    expect(document.body.textContent).toContain("2026-10-01 00:00:00 UTC");
    expect(document.body.textContent).not.toContain("拉取间隔");
    okButton().click();
    await flushPromises();
    expect(api.updateArchiveTask).not.toHaveBeenCalled();
  });

  it("关掉启用后保存提交 paused", async () => {
    mountEditor({ task: task() });
    await flushPromises();
    (wrapper.vm as unknown as { form: { enabled: boolean } }).form.enabled = false;
    okButton().click();
    await flushPromises();
    expect(api.updateArchiveTask).toHaveBeenCalledWith({ dataset_id: "ds_1m", status: "paused" });
  });
});

describe("新建时的回补起点", () => {
  const datasets = [{ dataset_id: "ds_1m", name: "现货分钟K线", freq: "1m", retention: "840h" }];
  const prepared = {
    freq: "1m",
    partition_unit: "day",
    purpose: "collect",
    suggested_dir_name: "binance-spot-kline-1m",
    dir_name_conflict: false
  };

  it("默认填线上保留期起点，提交带上它；说明写明每分钟跟随与首次整段读", async () => {
    api.prepareArchiveTask.mockResolvedValue({ ...prepared, retention: "840h", earliest_backfill_from: EARLIEST });
    api.createArchiveTask.mockClear();
    mountEditor({ mode: "create", task: null, presetDatasetId: "ds_1m", datasets, spaceId: "crypto" });
    await flushPromises();
    expect(document.body.textContent).toContain("创建后每分钟按 Storage 的变更日志跟随");
    okButton().click();
    await flushPromises();
    expect(api.createArchiveTask).toHaveBeenCalledWith(expect.objectContaining({ dataset_id: "ds_1m", backfill_from: EARLIEST }));
  });

  it("线上永久保留时必须填写回补起点", async () => {
    api.prepareArchiveTask.mockResolvedValue({ ...prepared, retention: "forever" });
    api.createArchiveTask.mockClear();
    mountEditor({ mode: "create", task: null, presetDatasetId: "ds_1m", datasets, spaceId: "crypto" });
    await flushPromises();
    expect(document.body.textContent).toContain("线上永久保留的数据集必须填写回补起点");
    okButton().click();
    await flushPromises();
    expect(api.createArchiveTask).not.toHaveBeenCalled();
  });
});

describe("新建时的目录名", () => {
  const datasets = [{ dataset_id: "ds_1m", name: "现货分钟K线", freq: "1m", retention: "840h" }];
  const prepared = {
    freq: "1m",
    partition_unit: "day",
    purpose: "collect",
    suggested_dir_name: "binance-spot-kline-1m",
    dir_name_conflict: false,
    earliest_backfill_from: EARLIEST
  };
  const dirInput = () => document.querySelector<HTMLInputElement>(".arco-drawer .arco-input-wrapper input")!;

  it("默认填建议值并显示完整路径预览，提交带上目录名", async () => {
    api.prepareArchiveTask.mockResolvedValue(prepared);
    api.createArchiveTask.mockClear();
    mountEditor({ mode: "create", task: null, presetDatasetId: "ds_1m", datasets, spaceId: "crypto" });
    await flushPromises();
    expect(api.prepareArchiveTask).toHaveBeenCalledWith("ds_1m", "");
    expect(dirInput().value).toBe("binance-spot-kline-1m");
    expect(document.body.textContent).toContain("建好后不能改");
    expect(document.body.textContent).toContain("crypto/collect/binance-spot-kline-1m/<yyyy>/<yyyymmdd>.csv.gz");
    okButton().click();
    await flushPromises();
    expect(api.createArchiveTask).toHaveBeenCalledWith(
      expect.objectContaining({ dataset_id: "ds_1m", dir_name: "binance-spot-kline-1m" })
    );
  });

  it("建议值已被占用时标红，不能提交", async () => {
    api.prepareArchiveTask.mockResolvedValue({ ...prepared, dir_name_conflict: true });
    api.createArchiveTask.mockClear();
    mountEditor({ mode: "create", task: null, presetDatasetId: "ds_1m", datasets, spaceId: "crypto" });
    await flushPromises();
    expect(document.body.textContent).toContain("目录名已被这个空间的其他任务使用，请改一个");
    okButton().click();
    await flushPromises();
    expect(api.createArchiveTask).not.toHaveBeenCalled();
  });

  it("改过的目录名防抖后带 dir_name 检查；不合规的不发请求", async () => {
    vi.useFakeTimers();
    api.prepareArchiveTask.mockResolvedValue(prepared);
    mountEditor({ mode: "create", task: null, presetDatasetId: "ds_1m", datasets, spaceId: "crypto" });
    await vi.advanceTimersByTimeAsync(0);
    await flushPromises();
    const vm = wrapper.vm as unknown as { form: { dirName: string } };
    vm.form.dirName = "Bad_Name";
    await vi.advanceTimersByTimeAsync(500);
    expect(api.prepareArchiveTask).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("只能用小写字母、数字和单个连字符");

    api.prepareArchiveTask.mockResolvedValue({ ...prepared, dir_name_conflict: true });
    vm.form.dirName = "binance-spot-1m";
    await vi.advanceTimersByTimeAsync(400);
    await flushPromises();
    expect(api.prepareArchiveTask).toHaveBeenLastCalledWith("ds_1m", "binance-spot-1m");
    expect(document.body.textContent).toContain("目录名已被这个空间的其他任务使用");
  });

  it("冲突时写出占用它的数据集；路径预览带归档根目录", async () => {
    api.prepareArchiveTask.mockResolvedValue({ ...prepared, dir_name_conflict: true });
    mountEditor({
      mode: "create",
      task: null,
      presetDatasetId: "ds_1m",
      datasets: [...datasets, { dataset_id: "ds_old", name: "旧分钟K线" }],
      tasks: [task({ dataset_id: "ds_old", purpose: "collect", dir_name: "binance-spot-kline-1m" })],
      spaceId: "crypto",
      rootDir: "/data/moox/archive"
    });
    await flushPromises();
    expect(document.body.textContent).toContain("目录名已被「旧分钟K线」使用，请改一个");
    api.prepareArchiveTask.mockResolvedValue({ ...prepared, dir_name_conflict: false });
    (wrapper.vm as unknown as { form: { dirName: string } }).form.dirName = "binance-spot-kline-1m-v2";
    await new Promise(resolve => setTimeout(resolve, 450));
    await flushPromises();
    expect(document.body.textContent).toContain(
      "/data/moox/archive/crypto/collect/binance-spot-kline-1m-v2/<yyyy>/<yyyymmdd>.csv.gz"
    );
  });

  it("上次检查失败时提交前重新检查", async () => {
    vi.useFakeTimers();
    api.prepareArchiveTask.mockResolvedValue(prepared);
    api.createArchiveTask.mockClear();
    mountEditor({ mode: "create", task: null, presetDatasetId: "ds_1m", datasets, spaceId: "crypto" });
    await vi.advanceTimersByTimeAsync(0);
    await flushPromises();
    api.prepareArchiveTask.mockRejectedValueOnce(new Error("超时"));
    (wrapper.vm as unknown as { form: { dirName: string } }).form.dirName = "binance-spot-1m";
    await vi.advanceTimersByTimeAsync(400);
    await flushPromises();
    expect(document.body.textContent).toContain("检查目录名失败：超时");
    okButton().click();
    await flushPromises();
    expect(api.prepareArchiveTask).toHaveBeenLastCalledWith("ds_1m", "binance-spot-1m");
    expect(api.createArchiveTask).toHaveBeenCalledWith(expect.objectContaining({ dir_name: "binance-spot-1m" }));
  });
});

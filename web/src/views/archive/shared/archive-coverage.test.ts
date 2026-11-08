import { flushPromises } from "@vue/test-utils";
import { effectScope, nextTick, ref } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ listArchiveTasks: vi.fn(), listDatasets: vi.fn() }));
vi.mock("@/api/archive", () => ({ listArchiveTasks: api.listArchiveTasks }));
vi.mock("@/api/storage/metadata", () => ({ listDatasets: api.listDatasets }));
import { coverageFor, invalidateArchiveCoverage, loadArchiveCoverage, useArchiveCoverage } from "./archive-coverage";

const dataset = (id: string, retention = "840h") => ({
  dataset_id: id,
  name: id,
  data_kind: "DATA_KIND_TIME_SERIES",
  status: "active",
  retention
});

beforeEach(() => {
  invalidateArchiveCoverage();
  api.listArchiveTasks.mockReset().mockResolvedValue({
    tasks: [
      { dataset_id: "ok_1m", health: "ok" },
      { dataset_id: "bad_1m", health: "error" }
    ]
  });
  api.listDatasets.mockReset().mockResolvedValue({
    datasets: [dataset("ok_1m"), dataset("bad_1m"), dataset("new_1m"), dataset("keep", "forever")],
    page_result: {}
  });
});
afterEach(() => vi.useRealTimers());

describe("采集任务页的归档状态", () => {
  it("有任务写已归档和健康等级，应归档但没任务写未归档，其余不显示", async () => {
    const data = await loadArchiveCoverage("crypto");
    expect(coverageFor(data, "ok_1m")).toEqual({ text: "已归档 · 正常", tone: "muted", link: "/archive/tasks?task=ok_1m" });
    expect(coverageFor(data, "bad_1m")).toMatchObject({ text: "已归档 · 异常", tone: "bad" });
    expect(coverageFor(data, "new_1m")).toEqual({ text: "未归档", tone: "warn", link: "/archive/tasks?create=new_1m" });
    expect(coverageFor(data, "keep")).toBeNull();
    expect(coverageFor(data, "")).toBeNull();
  });

  it("任一读取失败（例如 archive-mgr 没部署）什么都不显示，不当成未归档", async () => {
    api.listArchiveTasks.mockRejectedValueOnce(new Error("404"));
    expect(coverageFor(await loadArchiveCoverage("crypto"), "new_1m")).toBeNull();
    api.listDatasets.mockRejectedValueOnce(new Error("超时"));
    expect(coverageFor(await loadArchiveCoverage("crypto"), "new_1m")).toBeNull();
  });

  it("读取都不弹全局提示", async () => {
    await loadArchiveCoverage("crypto");
    expect(api.listArchiveTasks.mock.calls[0][1]).toEqual({ silentError: true });
    expect(api.listDatasets.mock.calls[0][1]).toEqual({ silentError: true });
  });

  it("同一空间共用缓存：60 秒内不重读，过期或作废后重读，切空间后重读", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(Date.parse("2026-11-07T00:00:00Z"));
    const space = ref("crypto");
    const scope = effectScope();
    const view = scope.run(() => useArchiveCoverage(space))!;
    const again = scope.run(() => useArchiveCoverage(space))!;
    await flushPromises();
    expect(api.listArchiveTasks).toHaveBeenCalledTimes(1);
    expect(view.value("new_1m")?.text).toBe("未归档");
    expect(again.value("new_1m")?.text).toBe("未归档");

    // 切空间：旧空间的结果不再显示，并按新空间重读
    space.value = "stockcn";
    await nextTick();
    expect(view.value("new_1m")).toBeNull();
    await flushPromises();
    expect(api.listArchiveTasks).toHaveBeenCalledTimes(2);

    // 作废后再挂载会重读；60 秒内再挂载不重读，过了 60 秒再挂载重读
    invalidateArchiveCoverage();
    scope.run(() => useArchiveCoverage(space));
    await flushPromises();
    expect(api.listArchiveTasks).toHaveBeenCalledTimes(3);
    scope.run(() => useArchiveCoverage(space));
    await flushPromises();
    expect(api.listArchiveTasks).toHaveBeenCalledTimes(3);
    vi.setSystemTime(Date.parse("2026-11-07T00:01:01Z"));
    scope.run(() => useArchiveCoverage(space));
    await flushPromises();
    expect(api.listArchiveTasks).toHaveBeenCalledTimes(4);
    scope.stop();
  });
});

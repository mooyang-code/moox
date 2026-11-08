import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ callControl: vi.fn(async () => ({})) }));
vi.mock("@/api/admin/http", () => ({ callControl: mocks.callControl }));
import * as archive from "./index";

beforeEach(() => mocks.callControl.mockClear());

const lastCall = () => mocks.callControl.mock.calls.at(-1) as unknown as [string, string, Record<string, unknown>, unknown];

describe("归档接口封装", () => {
  it("服务名是 archive，方法名和请求体对得上，请求体里没有 space_id", async () => {
    const cases: [() => Promise<unknown>, string, Record<string, unknown>][] = [
      [() => archive.getArchiveOverview(), "GetArchiveOverview", {}],
      [() => archive.listArchiveTasks({ page: 1, size: 100 }), "ListArchiveTasks", { page: { page: 1, size: 100 } }],
      [() => archive.prepareArchiveTask("ds"), "PrepareArchiveTask", { dataset_id: "ds" }],
      [
        () => archive.previewArchiveSchedule({ dataset_id: "ds", pull_every_bars: 5 }),
        "PreviewArchiveSchedule",
        { dataset_id: "ds", pull_every_bars: 5 }
      ],
      [
        () => archive.createArchiveTask({ dataset_id: "ds", pull_every_bars: 1, backfill_from: "", paused: false }),
        "CreateArchiveTask",
        { dataset_id: "ds", pull_every_bars: 1, backfill_from: "", paused: false }
      ],
      [
        () => archive.updateArchiveTask({ dataset_id: "ds", status: "paused" }),
        "UpdateArchiveTask",
        { dataset_id: "ds", status: "paused" }
      ],
      [() => archive.deleteArchiveTask("ds"), "DeleteArchiveTask", { dataset_id: "ds" }],
      [() => archive.runArchiveTaskNow("ds"), "RunArchiveTaskNow", { dataset_id: "ds" }],
      [
        () => archive.backfillArchiveRange({ dataset_id: "ds", range_start: "a", range_end: "b" }),
        "BackfillArchiveRange",
        { dataset_id: "ds", range_start: "a", range_end: "b" }
      ],
      [() => archive.cancelArchiveRun("r1"), "CancelArchiveRun", { run_id: "r1" }],
      [
        () => archive.listArchiveRuns({ dataset_id: "ds", include_ticks: true, page: { page: 1, size: 50 } }),
        "ListArchiveRuns",
        { dataset_id: "ds", include_ticks: true, page: { page: 1, size: 50 } }
      ],
      [
        () => archive.listArchivePartitions({ dataset_id: "ds", from_month: "2026-10", page: { page: 1, size: 50 } }),
        "ListArchivePartitions",
        { dataset_id: "ds", from_month: "2026-10", page: { page: 1, size: 50 } }
      ],
      [
        () => archive.getArchivePartition("ds", "2026-11-07"),
        "GetArchivePartition",
        { dataset_id: "ds", partition_key: "2026-11-07" }
      ]
    ];
    expect(cases).toHaveLength(13);
    for (const [run, method, body] of cases) {
      await run();
      const [service, called, req] = lastCall();
      expect(service).toBe("archive");
      expect(called).toBe(method);
      expect(req).toEqual(body);
      expect(req).not.toHaveProperty("space_id");
    }
  });

  it("轮询用的读取把 silentError 传下去", async () => {
    await archive.getArchiveOverview({ silentError: true });
    expect(lastCall()[3]).toEqual({ silentError: true });
    await archive.listArchiveTasks({ page: 1, size: 100 }, { silentError: true });
    expect(lastCall()[3]).toEqual({ silentError: true });
  });
});

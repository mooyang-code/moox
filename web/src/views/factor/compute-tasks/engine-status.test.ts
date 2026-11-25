import { describe, expect, it } from "vitest";
import type { EngineStatus, FactorTask, FactorTaskInfo, RecalcJob } from "@/api/factor/types";
import { recalcJobTarget, recalcQueryTasks, splitRecalcJobs, statsStrip } from "./engine-status";

const NOW = Date.parse("2026-10-05T12:00:00Z");

const info = (patch: Partial<FactorTask> = {}): FactorTaskInfo => ({
  factor_task: {
    task_id: "task_a",
    space_id: "crypto",
    source_dataset_id: "dataset_kline",
    freq: "1m",
    result_dataset_id: "dataset_factor_task_a",
    status: "enabled",
    ...patch
  },
  members: []
});

const job = (patch: Partial<RecalcJob> = {}): RecalcJob => ({
  job_id: "job-1",
  request_id: "factor-recalc-1",
  task_id: "task_a",
  factor_ids: ["Bias"],
  subjects: [],
  start_time: "2026-10-01T00:00:00Z",
  end_time: "2026-10-03T00:00:00Z",
  status: "running",
  progress_time: "2026-10-02T00:00:00Z",
  error: "",
  created_at: "1791198000000",
  updated_at: "1791199800000",
  ...patch
});

const onlineEngine = {
  engine_id: "factor-engine@mac",
  boot_id: "b",
  version: "v1",
  online: true,
  last_heartbeat_at: "",
  catalog_hash: "h",
  catalog_synced_at: "",
  catalog_in_sync: true
};

describe("计算任务列表顶部的引擎状态", () => {
  it("stats strip from engine status", () => {
    const engine = {
      engine: onlineEngine,
      consumer_running: true,
      compute_workers: 4,
      engine_expr_version: "expr/1",
      manager_expr_version: "expr/1",
      runners: [
        { task_id: "a", queued: 3, active: true },
        { task_id: "b", queued: 2, active: false }
      ]
    } as EngineStatus;
    const stats = statsStrip(engine, 2);
    expect(stats.map(item => item.key)).toEqual(["engine", "consumer", "compute", "queue", "warmup", "recalc"]);
    expect(stats[0]).toMatchObject({ value: "在线", tone: "ok" });
    expect(stats[1]).toMatchObject({ value: "运行中", tone: "ok" });
    expect(stats[2]).toMatchObject({ value: "4 线程", hint: "expr 版本 expr/1", tone: "info" });
    // 引擎与管理端的 expr 语义版本不一致：二者没有一起升级，标红提示。
    const mismatch = statsStrip({ ...engine, engine_expr_version: "expr/0" }, 0)[2];
    expect(mismatch).toMatchObject({ tone: "danger" });
    expect(mismatch.hint).toContain("版本不一致");
    expect(statsStrip(null, 0)[2]).toMatchObject({ value: "-", tone: "muted" });
    expect(stats[3].value).toBe("5");
    expect(stats[3].hint).toContain("1");
    expect(stats[4]).toMatchObject({ value: "已就绪", tone: "ok" });
    expect(stats[5].value).toBe("2");
    expect(statsStrip(null, 0)[1]).toMatchObject({ value: "-", tone: "muted" });
    expect(statsStrip(null, 0)[0]).toMatchObject({ value: "未连接", tone: "muted" });
    expect(statsStrip({ ...engine, consumer_running: false }, 0)[1]).toMatchObject({ value: "已停止", tone: "danger" });
    // 离线或从未收到心跳：只剩最后一次快照，不能展示成当前运行状态。
    const offline = statsStrip({ ...engine, engine: { ...onlineEngine, online: false } }, 0);
    expect(offline[1]).toMatchObject({ value: "-", tone: "muted" });
    expect(offline[4]).toMatchObject({ value: "-", tone: "muted" });
    expect(statsStrip({ ...engine, engine: undefined } as unknown as EngineStatus, 0)[1]).toMatchObject({ value: "-" });
  });

  it("warm-up card summarizes warming sets", () => {
    const engine = {
      engine: onlineEngine,
      consumer_running: true,
      compute_workers: 4,
      runners: [
        { task_id: "a", queued: 0, active: false, warmup_state: "warming", warm_subjects: 120, expected_subjects: 500 },
        { task_id: "b", queued: 0, active: false, warmup_state: "ready", warm_subjects: 500, expected_subjects: 500 },
        { task_id: "c", queued: 0, active: false, warmup_state: "warming" }
      ]
    } as EngineStatus;
    const warmup = statsStrip(engine, 0).find(item => item.key === "warmup");
    expect(warmup).toMatchObject({ value: "预热中", tone: "info", hint: "2 个计算任务 · 120 / 500 个对象就绪" });
    expect(statsStrip(null, 0).find(item => item.key === "warmup")).toMatchObject({ value: "-", tone: "muted" });
  });

  it("engine card follows the heartbeat", () => {
    const base = { consumer_running: true, compute_workers: 8, runners: [] } as unknown as EngineStatus;
    const engine = {
      engine_id: "factor-engine@mac",
      boot_id: "b",
      version: "v1",
      online: true,
      last_heartbeat_at: "",
      catalog_hash: "h",
      catalog_synced_at: "",
      catalog_in_sync: true
    };
    expect(statsStrip({ ...base, engine }, 0)[0]).toMatchObject({ value: "在线", tone: "ok", hint: "factor-engine@mac" });
    expect(statsStrip({ ...base, engine: { ...engine, catalog_in_sync: false } }, 0)[0]).toMatchObject({
      value: "在线",
      hint: "factor-engine@mac · 引擎目录未同步"
    });
    expect(statsStrip({ ...base, engine: { ...engine, online: false } }, 0)[0]).toMatchObject({ value: "离线", tone: "danger" });
  });

  it("补算只对已启用的计算任务查询", () => {
    const sets = [info({ task_id: "a" }), info({ task_id: "b", status: "disabled" }), info({ task_id: "c", status: "pending" })];
    expect(recalcQueryTasks(sets)).toEqual(["a"]);
  });

  it("补算作业分成进行中的与近 24 小时失败的", () => {
    const jobs = [
      job({ job_id: "r", status: "running" }),
      job({ job_id: "a", status: "accepted" }),
      job({ job_id: "f", status: "failed" }),
      job({ job_id: "old", status: "failed", updated_at: "1790812800000" }),
      job({ job_id: "s", status: "succeeded" })
    ];
    const split = splitRecalcJobs(jobs, NOW);
    expect(split.running.map(item => item.job_id)).toEqual(["r", "a"]);
    expect(split.recentFailed.map(item => item.job_id)).toEqual(["f"]);
    expect(recalcJobTarget(jobs[2])).toEqual({ path: "/factor/tasks", query: { tab: "recalc", task: "task_a", job: "f" } });
  });
});

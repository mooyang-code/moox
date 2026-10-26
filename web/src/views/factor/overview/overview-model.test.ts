import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { EngineStatus, FactorMember, FactorTask, FactorTaskInfo, RecalcJob } from "@/api/factor/types";
import { cardLinks, pendingItems, recalcQueryTasks, splitRecalcJobs, statsStrip, taskCards } from "./overview-model";

const NOW = Date.parse("2026-10-05T12:00:00Z");

const set = (patch: Partial<FactorTask> = {}): FactorTask => ({
  task_id: "task_a",
  space_id: "crypto",
  source_dataset_id: "dataset_kline",
  freq: "1m",
  result_dataset_id: "dataset_factor_task_a",
  status: "enabled",
  ...patch
});

const member = (factorId: string, status: FactorMember["status"] = "enabled"): FactorMember => ({
  task_id: "task_a",
  factor_id: factorId,
  status,
  factor: { factor_id: factorId } as FactorMember["factor"],
  created_at: "",
  updated_at: ""
});

const info = (patch: Partial<FactorTask> = {}, extra: Partial<FactorTaskInfo> = {}): FactorTaskInfo => ({
  factor_task: set(patch),
  members: [],
  ...extra
});

const run = (patch: Partial<NonNullable<FactorTaskInfo["last_run"]>> = {}): NonNullable<FactorTaskInfo["last_run"]> => ({
  task_id: "task_a",
  last_period_time: 1_700_000_000,
  last_status: "complete",
  lag_seconds: 10,
  ...patch
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
  created_at: "2026-10-05T11:00:00Z",
  updated_at: "2026-10-05T11:30:00Z",
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

const label = (item: FactorTask) => `现货K线 · ${item.freq}`;

describe("overview model", () => {
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

  it("task cards list member chips from last_run.factors", () => {
    const value = info(
      {},
      {
        members: [member("A"), member("B"), member("C"), member("D", "disabled")],
        last_run: run({
          factors: [
            { factor_id: "A", status: "complete" },
            { factor_id: "B", status: "degraded", failed_subjects: ["x", "y"] },
            { factor_id: "C", status: "skipped" }
          ]
        })
      }
    );
    const [card] = taskCards([value], label);
    expect(card.title).toBe("现货K线 · 1m");
    expect(card.health.key).toBe("ok");
    expect(card.chips.map(chip => [chip.factorId, chip.label])).toEqual([
      ["A", "正常"],
      ["B", "降级 · 2 个对象失败"],
      ["C", "已跳过"],
      ["D", "已停用"]
    ]);
  });

  it("pending items derivation", () => {
    const failed = info({ task_id: "f" }, { last_run: run({ task_id: "f", last_status: "failed" }) });
    const degraded = info({ task_id: "d" }, { last_run: run({ task_id: "d", last_status: "degraded", failed_subjects: ["x"] }) });
    const lagging = info({ task_id: "l" }, { last_run: run({ task_id: "l", lag_seconds: 500 }) });
    const creating = info({ task_id: "p", status: "pending" });
    const healthy = info({ task_id: "ok" }, { last_run: run({ task_id: "ok" }) });
    const items = pendingItems(
      [failed, degraded, lagging, creating, healthy],
      [job({ job_id: "bad", task_id: "ok", status: "failed", error: "boom" })],
      label,
      NOW
    );
    expect(items.map(item => item.kind)).toEqual(["failed", "degraded", "lagging", "pending", "recalc-failed"]);
    expect(items[0].target).toEqual({ path: "/factor/tasks", query: { detail: "f" } });
    expect(items[1].target).toEqual({ path: "/factor/tasks", query: { tab: "results", task: "d" } });
    expect(items[1].text).toContain("1");
    expect(items[2].target.query).toEqual({ tab: "results", task: "l" });
    expect(items[3].target).toEqual({ path: "/factor/tasks", query: { detail: "p" } });
    expect(items[4].target).toEqual({ path: "/factor/tasks", query: { tab: "recalc", task: "ok", job: "bad" } });
  });

  it("ignores recalc failures older than a day and cancelled jobs", () => {
    const old = job({ status: "failed", updated_at: "2026-10-01T00:00:00Z" });
    const cancelled = job({ status: "cancelled" });
    expect(pendingItems([info()], [old, cancelled], label, NOW)).toEqual([]);
  });

  it("running recalc only queries enabled sets", () => {
    const sets = [
      info({ task_id: "a" }),
      info({ task_id: "b", status: "disabled" }),
      info({ task_id: "c", status: "pending" }),
      info({ task_id: "d" })
    ];
    expect(recalcQueryTasks(sets)).toEqual(["a", "d"]);
  });

  it("splits fetched recalc jobs into running and failed", () => {
    const jobs = [
      job({ job_id: "r", status: "running" }),
      job({ job_id: "a", status: "accepted" }),
      job({ job_id: "f", status: "failed" }),
      job({ job_id: "s", status: "succeeded" })
    ];
    const split = splitRecalcJobs(jobs);
    expect(split.running.map(item => item.job_id)).toEqual(["r", "a"]);
    expect(split.failed.map(item => item.job_id)).toEqual(["f"]);
  });

  it("links target the right tab and carry ?task= / ?detail=", () => {
    expect(cardLinks("task_a")).toEqual({
      detail: { path: "/factor/tasks", query: { detail: "task_a" } },
      results: { path: "/factor/tasks", query: { tab: "results", task: "task_a" } },
      recalc: { path: "/factor/tasks", query: { tab: "recalc", task: "task_a" } },
      factors: { path: "/factor/tasks", query: { detail: "task_a" } }
    });
  });
});

describe("overview page contract", () => {
  const page = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");

  it("overview has no write action", () => {
    for (const name of [
      "createFactorTask",
      "deleteFactorTask",
      "setFactorTaskStatus",
      "setFactorMemberStatus",
      "addFactorToTask",
      "removeFactorFromTask",
      "recalcFactors",
      "cancelRecalcJob",
      "createFactor",
      "deleteFactor"
    ]) {
      expect(page).not.toContain(name);
    }
  });

  it("fans out recalc queries per enabled set and polls through usePolling", () => {
    expect(page).toContain("listRecalcJobs");
    expect(page).toContain("recalcQueryTasks");
    expect(page).toContain("usePolling");
  });
});

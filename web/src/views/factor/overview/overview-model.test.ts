import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { EngineStatus, FactorMember, FactorSet, FactorSetInfo, RecalcJob } from "@/api/factor/types";
import { cardLinks, pendingItems, recalcQuerySets, splitRecalcJobs, statsStrip, taskCards } from "./overview-model";

const NOW = Date.parse("2026-10-05T12:00:00Z");

const set = (patch: Partial<FactorSet> = {}): FactorSet => ({
  set_id: "set_a",
  space_id: "crypto",
  source_dataset_id: "dataset_kline",
  freq: "1m",
  subject_mode: "all",
  subjects: [],
  result_dataset_id: "dataset_factor_set_a",
  status: "enabled",
  ...patch
});

const member = (factorId: string, status: FactorMember["status"] = "enabled"): FactorMember => ({
  set_id: "set_a",
  factor_id: factorId,
  status,
  factor: { factor_id: factorId } as FactorMember["factor"],
  created_at: "",
  updated_at: ""
});

const info = (patch: Partial<FactorSet> = {}, extra: Partial<FactorSetInfo> = {}): FactorSetInfo => ({
  factor_set: set(patch),
  members: [],
  ...extra
});

const run = (patch: Partial<NonNullable<FactorSetInfo["last_run"]>> = {}): NonNullable<FactorSetInfo["last_run"]> => ({
  set_id: "set_a",
  last_period_time: 1_700_000_000,
  last_status: "complete",
  lag_seconds: 10,
  ...patch
});

const job = (patch: Partial<RecalcJob> = {}): RecalcJob => ({
  job_id: "job-1",
  request_id: "factor-recalc-1",
  set_id: "set_a",
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

const label = (item: FactorSet) => `现货K线 · ${item.freq}`;

describe("overview model", () => {
  it("stats strip from engine status", () => {
    const engine = {
      consumer_running: true,
      python_workers: 4,
      python_busy: 1,
      lanes: [
        { set_id: "a", queued: 3, active: true },
        { set_id: "b", queued: 2, active: false }
      ]
    } as EngineStatus;
    const stats = statsStrip(engine, 2);
    expect(stats.map(item => item.key)).toEqual(["engine", "consumer", "workers", "queue", "recalc"]);
    expect(stats[0]).toMatchObject({ value: "未连接", tone: "danger" });
    expect(stats[1]).toMatchObject({ value: "运行中", tone: "ok" });
    expect(stats[2].value).toBe("1 / 4");
    expect(stats[3].value).toBe("5");
    expect(stats[3].hint).toContain("1");
    expect(stats[4].value).toBe("2");
    expect(statsStrip(null, 0)[1]).toMatchObject({ value: "-", tone: "muted" });
    expect(statsStrip(null, 0)[0]).toMatchObject({ value: "未连接", tone: "muted" });
    expect(statsStrip({ ...engine, consumer_running: false }, 0)[1]).toMatchObject({ value: "已停止", tone: "danger" });
  });

  it("engine card follows the heartbeat", () => {
    const base = { consumer_running: true, python_workers: 8, python_busy: 0, lanes: [] } as unknown as EngineStatus;
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
    const failed = info({ set_id: "f" }, { last_run: run({ set_id: "f", last_status: "failed" }) });
    const degraded = info({ set_id: "d" }, { last_run: run({ set_id: "d", last_status: "degraded", failed_subjects: ["x"] }) });
    const lagging = info({ set_id: "l" }, { last_run: run({ set_id: "l", lag_seconds: 500 }) });
    const creating = info({ set_id: "p", status: "pending" });
    const healthy = info({ set_id: "ok" }, { last_run: run({ set_id: "ok" }) });
    const items = pendingItems(
      [failed, degraded, lagging, creating, healthy],
      [job({ job_id: "bad", set_id: "ok", status: "failed", error: "boom" })],
      label,
      NOW
    );
    expect(items.map(item => item.kind)).toEqual(["failed", "degraded", "lagging", "pending", "recalc-failed"]);
    expect(items[0].target).toEqual({ path: "/factor/tasks", query: { detail: "f" } });
    expect(items[1].target).toEqual({ path: "/factor/tasks", query: { tab: "results", set: "d" } });
    expect(items[1].text).toContain("1");
    expect(items[2].target.query).toEqual({ tab: "results", set: "l" });
    expect(items[3].target).toEqual({ path: "/factor/tasks", query: { detail: "p" } });
    expect(items[4].target).toEqual({ path: "/factor/tasks", query: { tab: "recalc", set: "ok", job: "bad" } });
  });

  it("ignores recalc failures older than a day and cancelled jobs", () => {
    const old = job({ status: "failed", updated_at: "2026-10-01T00:00:00Z" });
    const cancelled = job({ status: "cancelled" });
    expect(pendingItems([info()], [old, cancelled], label, NOW)).toEqual([]);
  });

  it("running recalc only queries enabled sets", () => {
    const sets = [
      info({ set_id: "a" }),
      info({ set_id: "b", status: "disabled" }),
      info({ set_id: "c", status: "pending" }),
      info({ set_id: "d" })
    ];
    expect(recalcQuerySets(sets)).toEqual(["a", "d"]);
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

  it("links target the right tab and carry ?set= / ?detail=", () => {
    expect(cardLinks("set_a")).toEqual({
      detail: { path: "/factor/tasks", query: { detail: "set_a" } },
      results: { path: "/factor/tasks", query: { tab: "results", set: "set_a" } },
      recalc: { path: "/factor/tasks", query: { tab: "recalc", set: "set_a" } },
      factors: { path: "/factor/tasks", query: { detail: "set_a" } }
    });
  });
});

describe("overview page contract", () => {
  const page = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");

  it("overview has no write action", () => {
    for (const name of [
      "createFactorSet",
      "deleteFactorSet",
      "setFactorSetStatus",
      "updateFactorSet",
      "setFactorMemberStatus",
      "addFactorToSet",
      "removeFactorFromSet",
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
    expect(page).toContain("recalcQuerySets");
    expect(page).toContain("usePolling");
  });
});

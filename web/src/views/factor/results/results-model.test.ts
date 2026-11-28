import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { FactorTask, FactorTaskInfo } from "@/api/factor/types";
import { resolveActiveTaskId, resultTasks, resultSummary } from "./results-model";

const set = (patch: Partial<FactorTask> = {}): FactorTask => ({
  task_id: "task_a",
  space_id: "crypto",
  source_dataset_id: "dataset_kline",
  frequency: "1m",
  result_dataset_id: "dataset_factor_task_a",
  status: "enabled",
  ...patch
});

const info = (patch: Partial<FactorTask> = {}, last_run?: FactorTaskInfo["last_run"]): FactorTaskInfo => ({
  factor_task: set(patch),
  members: [],
  last_run
});

describe("results model", () => {
  it("pending sets are excluded, disabled sets are labeled", () => {
    const sets = [
      info({ task_id: "a" }),
      info({ task_id: "b", status: "pending" }),
      info({ task_id: "c", status: "disabled" }),
      info({ task_id: "d", status: "deleting" })
    ];
    expect(resultTasks(sets).map(item => item.factor_task.task_id)).toEqual(["a", "c"]);
  });

  it("status line extras: last period and normal/degraded counts", () => {
    const value = info(
      {},
      {
        task_id: "task_a",
        last_period_time: 1_700_000_000,
        last_status: "degraded",
        lag_seconds: 5,
        factors: [
          { factor_id: "A", status: "complete" },
          { factor_id: "B", status: "complete" },
          { factor_id: "C", status: "degraded" },
          { factor_id: "D", status: "skipped" }
        ]
      }
    );
    expect(resultSummary(value, "UTC")).toMatchObject({ total: 4, complete: 2, degraded: 1, skipped: 1, hasPeriod: true });
    expect(resultSummary(info(), "UTC")).toMatchObject({ total: 0, hasPeriod: false });
  });

  it("resolves the active set: preferred, else first available", () => {
    const sets = [info({ task_id: "a" }), info({ task_id: "c", status: "disabled" })];
    expect(resolveActiveTaskId(sets, "c")).toBe("c");
    expect(resolveActiveTaskId(sets, "missing")).toBe("a");
    expect(resolveActiveTaskId([], "a")).toBe("");
  });
});

describe("results page contract", () => {
  const page = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");

  it("uses result-toolbar and ViewBrowse status-extra slot, not status-strip", () => {
    expect(page).toContain("result-toolbar");
    expect(page).toContain("#status-extra");
    expect(page).not.toContain("embedded");
    expect(page).not.toContain("status-strip");
  });

  it("no explanation row under the tabs", () => {
    expect(page).not.toContain("results-tip");
    expect(page).not.toContain('<a-alert type="info"');
  });
});

import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { FactorTask, FactorTaskInfo } from "@/api/factor/types";
import type { View } from "@/api/storage/types";
import { pickResultView, resolveActiveTaskId, resultTasks, resultSummary, resultTabTitle } from "./results-model";

const set = (patch: Partial<FactorTask> = {}): FactorTask => ({
  task_id: "task_a",
  space_id: "crypto",
  source_dataset_id: "dataset_kline",
  freq: "1m",
  result_dataset_id: "dataset_factor_task_a",
  status: "enabled",
  ...patch
});

const info = (patch: Partial<FactorTask> = {}, last_run?: FactorTaskInfo["last_run"]): FactorTaskInfo => ({
  factor_task: set(patch),
  members: [],
  last_run
});

const view = (patch: Partial<View> & { role?: string } = {}): View =>
  ({
    space_id: "crypto",
    view_id: "view_factor_task_a",
    dataset_id: "dataset_factor_task_a",
    status: "active",
    attributes: { view_role: "factor_result", owner_module: "factor" },
    ...patch
  }) as View;

describe("results model", () => {
  it("picks the factor_result view of the result dataset", () => {
    const views = [
      view({ view_id: "other_dataset", dataset_id: "dataset_other" }),
      view({ view_id: "wrong_role", attributes: { view_role: "collection_browse" } }),
      view({ view_id: "ok" })
    ];
    expect(pickResultView(views, set())?.view_id).toBe("ok");
    expect(pickResultView([], set())).toBeNull();
    expect(pickResultView([view({ dataset_id: "dataset_x" })], set())).toBeNull();
  });

  it("pending sets are excluded, disabled sets are labeled", () => {
    const sets = [
      info({ task_id: "a" }),
      info({ task_id: "b", status: "pending" }),
      info({ task_id: "c", status: "disabled" }),
      info({ task_id: "d", status: "deleting" })
    ];
    expect(resultTasks(sets).map(item => item.factor_task.task_id)).toEqual(["a", "c"]);
    expect(resultTabTitle(sets[0], "现货K线 · 1m")).toBe("现货K线 · 1m");
    expect(resultTabTitle(sets[2], "现货K线 · 1m")).toBe("现货K线 · 1m（已停用）");
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
    expect(resultSummary(value)).toMatchObject({ total: 4, complete: 2, degraded: 1, skipped: 1, hasPeriod: true });
    expect(resultSummary(info())).toMatchObject({ total: 0, hasPeriod: false });
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
    expect(page).toContain(':embedded="true"');
    expect(page).not.toContain("status-strip");
  });

  it("scopes to the factor result view of the selected set", () => {
    const model = fs.readFileSync(path.resolve(__dirname, "results-model.ts"), "utf8");
    expect(model).toContain('view.attributes?.view_role === "factor_result"');
    expect(model).toContain("result_dataset_id");
    expect(page).not.toContain("ViewDefinitions");
  });

  it("no explanation row under the tabs", () => {
    expect(page).not.toContain("results-tip");
    expect(page).not.toContain('<a-alert type="info"');
  });
});

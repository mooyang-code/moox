import { describe, expect, it } from "vitest";
import type { FactorMember, FactorTask, FactorTaskInfo } from "@/api/factor/types";
import {
  addFactorState,
  deleteState,
  filterTasks,
  memberActions,
  memberCountText,
  memberCounts,
  memberPeriodState,
  setActionKind
} from "./compute-tasks-model";

const set = (patch: Partial<FactorTask> = {}): FactorTask => ({
  task_id: "task_a",
  space_id: "crypto",
  source_dataset_id: "dataset_kline",
  freq: "1m",
  result_dataset_id: "dataset_factor_task_a",
  status: "enabled",
  ...patch
});

const member = (factorId: string, status: FactorMember["status"]): FactorMember => ({
  task_id: "task_a",
  factor_id: factorId,
  status,
  factor: { factor_id: factorId } as FactorMember["factor"],
  created_at: "",
  updated_at: ""
});

const info = (patch: Partial<FactorTask> = {}, members: FactorMember[] = []): FactorTaskInfo => ({
  factor_task: set(patch),
  members
});
const labelOf = (item: FactorTask) =>
  `${item.source_dataset_id === "dataset_kline" ? "现货K线" : item.source_dataset_id} · ${item.freq}`;

describe("compute task list model", () => {
  it("filterTasks by keyword and status", () => {
    const sets = [
      info({ task_id: "task_a" }),
      info({ task_id: "task_b", source_dataset_id: "dataset_funding", status: "disabled" })
    ];
    expect(filterTasks(sets, { keyword: "", status: "" }, labelOf)).toHaveLength(2);
    expect(filterTasks(sets, { keyword: "现货", status: "" }, labelOf).map(item => item.factor_task.task_id)).toEqual(["task_a"]);
    expect(filterTasks(sets, { keyword: "TASK_B", status: "" }, labelOf).map(item => item.factor_task.task_id)).toEqual([
      "task_b"
    ]);
    expect(filterTasks(sets, { keyword: "funding", status: "" }, labelOf)).toHaveLength(1);
    expect(filterTasks(sets, { keyword: "", status: "disabled" }, labelOf).map(item => item.factor_task.task_id)).toEqual([
      "task_b"
    ]);
  });

  it("factor counts are enabled over total from members", () => {
    const value = info({}, [member("a", "enabled"), member("b", "disabled"), member("c", "enabled")]);
    expect(memberCounts(value)).toEqual({ enabled: 2, total: 3 });
    expect(memberCountText(value)).toBe("2 / 3");
    expect(memberCountText(info())).toBe("0 / 0");
  });

  it("row status action follows the set status", () => {
    expect(setActionKind(set({ status: "enabled" }))).toBe("disable");
    expect(setActionKind(set({ status: "disabled" }))).toBe("enable");
    expect(setActionKind(set({ status: "pending" }))).toBe("retry");
    expect(setActionKind(set({ status: "deleting" }))).toBe("none");
  });

  it("delete disabled while members exist and explains why", () => {
    const blocked = deleteState(info({ status: "disabled" }, [member("a", "disabled")]));
    expect(blocked.disabled).toBe(true);
    expect(blocked.reason).toContain("移除");
    expect(deleteState(info({ status: "disabled" })).disabled).toBe(false);
    // 后端 DeleteTask 要求任务已停用：启用中的任务先停用。
    expect(deleteState(info({ status: "enabled" }))).toMatchObject({ disabled: true, reason: expect.stringContaining("停用") });
    expect(deleteState(info({ status: "deleting" })).disabled).toBe(true);
  });

  it("member actions: edit and remove disabled when enabled", () => {
    const enabled = memberActions(set(), member("a", "enabled"));
    expect(enabled.edit.disabled).toBe(true);
    expect(enabled.edit.reason).toContain("停用");
    expect(enabled.remove.disabled).toBe(true);
    expect(enabled.remove.reason).toContain("停用");
    const disabled = memberActions(set(), member("a", "disabled"));
    expect(disabled.edit.disabled).toBe(false);
    expect(disabled.remove.disabled).toBe(false);
  });

  it("toggle label follows member status", () => {
    expect(memberActions(set(), member("a", "enabled"))).toMatchObject({ toggleLabel: "停用", toggleTarget: "disabled" });
    expect(memberActions(set(), member("a", "disabled"))).toMatchObject({ toggleLabel: "启用", toggleTarget: "enabled" });
  });

  it("add factor disabled for pending or deleting sets", () => {
    expect(addFactorState(set()).disabled).toBe(false);
    expect(addFactorState(set({ status: "pending" })).disabled).toBe(true);
    expect(addFactorState(set({ status: "deleting" })).disabled).toBe(true);
    expect(addFactorState(set({ status: "disabled" })).disabled).toBe(false);
    expect(memberActions(set({ status: "pending" }), member("a", "disabled")).toggle.disabled).toBe(true);
    // 已停用的计算任务里不能启用成员（后端启用回填要求任务已启用），但仍可停用成员。
    expect(memberActions(set({ status: "disabled" }), member("a", "disabled")).toggle.disabled).toBe(true);
    expect(memberActions(set({ status: "disabled" }), member("a", "enabled")).toggle.disabled).toBe(false);
  });

  it("finds the latest period state of a member factor", () => {
    const value: FactorTaskInfo = {
      ...info({}, [member("a", "enabled")]),
      last_run: {
        task_id: "task_a",
        last_period_time: 1,
        last_status: "complete",
        lag_seconds: 0,
        factors: [{ factor_id: "a", status: "degraded" }]
      }
    };
    expect(memberPeriodState(value, "a")?.status).toBe("degraded");
    expect(memberPeriodState(value, "b")).toBeUndefined();
  });
});

describe("enable hint", () => {
  it("tells the operator the engine applies an enable within a minute without backfilling the gap", async () => {
    const { ENABLE_ENGINE_HINT } = await import("./compute-tasks-model");
    expect(ENABLE_ENGINE_HINT).toContain("约 1 分钟内生效");
    expect(ENABLE_ENGINE_HINT).toContain("不会自动补算");
  });
});

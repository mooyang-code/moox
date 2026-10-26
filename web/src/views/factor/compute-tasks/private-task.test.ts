import { describe, expect, it } from "vitest";
import type { FactorTaskInfo } from "@/api/factor/types";
import { addFactorState, isPrivateTask, memberActions, memberPeriodState, paramsSourceLabel } from "./compute-tasks-model";

describe("private factor tasks", () => {
  const privateTask = { status: "enabled" as const, owner_instance_id: "inst-1" };
  const sharedTask = { status: "enabled" as const };

  it("blocks manual maintenance of an instance-owned set", () => {
    expect(isPrivateTask(privateTask)).toBe(true);
    expect(isPrivateTask(sharedTask)).toBe(false);
    const add = addFactorState(privateTask);
    expect(add.disabled).toBe(true);
    expect(add.reason).toContain("私有因子任务");
    const actions = memberActions(privateTask, { status: "disabled" });
    expect(
      [actions.toggle, actions.edit, actions.remove].every(state => state.disabled && state.reason.includes("实例维护"))
    ).toBe(true);
    expect(addFactorState(sharedTask).disabled).toBe(false);
    expect(memberActions(sharedTask, { status: "disabled" }).remove.disabled).toBe(false);
  });
});

describe("member period state", () => {
  const info = {
    last_run: {
      task_id: "s",
      last_period_time: 0,
      last_status: "complete",
      lag_seconds: 0,
      factors: [
        { factor_id: "bias", member_alias: "fast", status: "complete", params_source: "requested" },
        { factor_id: "bias", member_alias: "slow", status: "degraded", params_source: "carried_offline" }
      ]
    }
  } as FactorTaskInfo;

  it("finds a period state by member alias, so one definition can appear twice", () => {
    expect(memberPeriodState(info, "fast")?.status).toBe("complete");
    expect(memberPeriodState(info, "slow")?.status).toBe("degraded");
    expect(memberPeriodState(info, "bias")).toBeUndefined();
  });

  it("falls back to the definition id for states without an alias", () => {
    const legacy = { last_run: { factors: [{ factor_id: "ma", status: "complete" }] } } as FactorTaskInfo;
    expect(memberPeriodState(legacy, "ma")?.status).toBe("complete");
  });

  it("labels the parameter source and hides unknown ones", () => {
    expect(paramsSourceLabel("requested")).toEqual({ label: "参数已更新", color: "green" });
    expect(paramsSourceLabel("carried_offline")?.label).toBe("离线沿用");
    expect(paramsSourceLabel("")).toBeNull();
    expect(paramsSourceLabel(undefined)).toBeNull();
  });
});

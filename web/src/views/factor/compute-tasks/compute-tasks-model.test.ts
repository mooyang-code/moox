import { describe, expect, it } from "vitest";
import type { FactorMember, FactorSet, FactorSetInfo } from "@/api/factor/types";
import {
  addFactorState,
  deleteState,
  filterSets,
  memberActions,
  memberCountText,
  memberCounts,
  memberPeriodState,
  scopeText,
  setActionKind
} from "./compute-tasks-model";

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

const member = (factorId: string, status: FactorMember["status"]): FactorMember => ({
  set_id: "set_a",
  factor_id: factorId,
  status,
  factor: { factor_id: factorId } as FactorMember["factor"],
  created_at: "",
  updated_at: ""
});

const info = (patch: Partial<FactorSet> = {}, members: FactorMember[] = []): FactorSetInfo => ({
  factor_set: set(patch),
  members
});
const labelOf = (item: FactorSet) =>
  `${item.source_dataset_id === "dataset_kline" ? "现货K线" : item.source_dataset_id} · ${item.freq}`;

describe("compute task list model", () => {
  it("filterSets by keyword and status", () => {
    const sets = [info({ set_id: "set_a" }), info({ set_id: "set_b", source_dataset_id: "dataset_funding", status: "disabled" })];
    expect(filterSets(sets, { keyword: "", status: "" }, labelOf)).toHaveLength(2);
    expect(filterSets(sets, { keyword: "现货", status: "" }, labelOf).map(item => item.factor_set.set_id)).toEqual(["set_a"]);
    expect(filterSets(sets, { keyword: "SET_B", status: "" }, labelOf).map(item => item.factor_set.set_id)).toEqual(["set_b"]);
    expect(filterSets(sets, { keyword: "funding", status: "" }, labelOf)).toHaveLength(1);
    expect(filterSets(sets, { keyword: "", status: "disabled" }, labelOf).map(item => item.factor_set.set_id)).toEqual(["set_b"]);
  });

  it("factor counts are enabled over total from members", () => {
    const value = info({}, [member("a", "enabled"), member("b", "disabled"), member("c", "enabled")]);
    expect(memberCounts(value)).toEqual({ enabled: 2, total: 3 });
    expect(memberCountText(value)).toBe("2 / 3");
    expect(memberCountText(info())).toBe("0 / 0");
  });

  it("scope text distinguishes all and included subjects", () => {
    expect(scopeText(set())).toBe("全部对象");
    expect(scopeText(set({ subject_mode: "include", subjects: ["BTC-USDT", "ETH-USDT"] }))).toBe("指定 2 个");
  });

  it("row status action follows the set status", () => {
    expect(setActionKind(set({ status: "enabled" }))).toBe("disable");
    expect(setActionKind(set({ status: "disabled" }))).toBe("enable");
    expect(setActionKind(set({ status: "pending" }))).toBe("retry");
    expect(setActionKind(set({ status: "deleting" }))).toBe("none");
  });

  it("delete disabled while members exist and explains why", () => {
    const blocked = deleteState(info({}, [member("a", "disabled")]));
    expect(blocked.disabled).toBe(true);
    expect(blocked.reason).toContain("移除");
    expect(deleteState(info()).disabled).toBe(false);
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
  });

  it("finds the latest period state of a member factor", () => {
    const value: FactorSetInfo = {
      ...info({}, [member("a", "enabled")]),
      last_run: {
        set_id: "set_a",
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

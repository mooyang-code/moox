import { describe, expect, it } from "vitest";
import { buildRunningQuery, emptyKind, isTrialInstance, listKey, parseRunningQuery } from "./running-model";

describe("running page model", () => {
  it("normalizes the URL state", () => {
    expect(parseRunningQuery({ page: "0", enabled: "maybe", strategy_id: " s1 " })).toEqual({
      strategyId: "s1",
      enabled: "",
      page: 1,
      instanceId: "",
      create: false
    });
    expect(parseRunningQuery({ enabled: "false", instance_id: "i1", page: "3" })).toMatchObject({
      enabled: "false",
      instanceId: "i1",
      page: 3
    });
  });

  it("keeps the create drawer and the quick detail mutually exclusive", () => {
    const state = parseRunningQuery({ create: "1", instance_id: "i1" });
    expect(state).toMatchObject({ create: true, instanceId: "" });
    expect(buildRunningQuery({ ...state, instanceId: "i1" })).toEqual({ create: "1" });
  });

  it("omits defaults when writing the URL", () => {
    expect(buildRunningQuery({ strategyId: "", enabled: "", page: 1, instanceId: "", create: false })).toEqual({});
    expect(buildRunningQuery({ strategyId: "s1", enabled: "true", page: 2, instanceId: "i1", create: false })).toEqual({
      enabled: "true",
      instance_id: "i1",
      page: "2",
      strategy_id: "s1"
    });
  });

  it("changes the list key only for list conditions", () => {
    const base = { strategyId: "s1", enabled: "" as const, page: 2, instanceId: "", create: false };
    expect(listKey({ ...base, instanceId: "i1" })).toBe(listKey(base));
    expect(listKey({ ...base, page: 3 })).not.toBe(listKey(base));
  });

  it("distinguishes empty states", () => {
    expect(emptyKind({ loading: true, error: "", count: 0, filtered: false })).toBe("none");
    expect(emptyKind({ loading: false, error: "x", count: 0, filtered: true })).toBe("error");
    expect(emptyKind({ loading: false, error: "", count: 0, filtered: true })).toBe("no-match");
    expect(emptyKind({ loading: false, error: "", count: 0, filtered: false })).toBe("no-instances");
    expect(emptyKind({ loading: false, error: "", count: 2, filtered: false })).toBe("none");
  });

  it("marks enabled, account-bound, unconfirmed instances as trial", () => {
    expect(isTrialInstance({ enabled: true, logical_account_id: "la-1", confirmed_at: "" })).toBe(true);
    expect(isTrialInstance({ enabled: true, logical_account_id: "la-1" })).toBe(true);
    expect(isTrialInstance({ enabled: true, logical_account_id: "la-1", confirmed_at: "2026-11-15T01:00:00Z" })).toBe(false);
    // 没挂账户的实例启用即确认；停用的实例不在试运行。
    expect(isTrialInstance({ enabled: true, logical_account_id: "", confirmed_at: "" })).toBe(false);
    expect(isTrialInstance({ enabled: false, logical_account_id: "la-1", confirmed_at: "" })).toBe(false);
    expect(isTrialInstance(null)).toBe(false);
  });
});

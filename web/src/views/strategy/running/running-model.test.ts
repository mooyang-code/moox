import { describe, expect, it } from "vitest";
import {
  buildRunningQuery,
  emptyKind,
  instanceDeleteState,
  listKey,
  pageAfterRemoval,
  parseRunningQuery
} from "./running-model";

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

  it("explains why an instance cannot be deleted", () => {
    expect(instanceDeleteState({ enabled: true, session_id: "s" })).toMatchObject({ disabled: true });
    expect(instanceDeleteState({ enabled: false, session_id: "s" }).reason).toContain("会话");
    expect(instanceDeleteState({ enabled: false, session_id: "" })).toEqual({ disabled: false, reason: "" });
  });

  it("distinguishes empty states", () => {
    expect(emptyKind({ loading: true, error: "", count: 0, filtered: false })).toBe("none");
    expect(emptyKind({ loading: false, error: "x", count: 0, filtered: true })).toBe("error");
    expect(emptyKind({ loading: false, error: "", count: 0, filtered: true })).toBe("no-match");
    expect(emptyKind({ loading: false, error: "", count: 0, filtered: false })).toBe("no-instances");
    expect(emptyKind({ loading: false, error: "", count: 2, filtered: false })).toBe("none");
  });

  it("steps back a page when the last page becomes empty", () => {
    expect(pageAfterRemoval(3, 0)).toBe(2);
    expect(pageAfterRemoval(1, 0)).toBe(1);
    expect(pageAfterRemoval(3, 4)).toBe(3);
  });
});

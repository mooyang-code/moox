import { describe, expect, it } from "vitest";
import { buildReplayQuery, emptyPrefill, parseReplayQuery } from "./replay-query";

describe("replay query", () => {
  it("normalizes pages and drops the definition when an instance is given", () => {
    expect(
      parseReplayQuery({ page: "0", bar_page: "x", strategy_id: "s1", instance_id: "i1", view_id: "v1", create: "1" })
    ).toEqual({
      replayId: "",
      page: 1,
      barPage: 1,
      create: true,
      prefill: { strategyId: "", instanceId: "i1", viewId: "v1" }
    });
  });

  it("round-trips the analysis state without defaults", () => {
    const state = parseReplayQuery({ replay_id: "r1", page: "2", bar_page: "3" });
    expect(buildReplayQuery(state)).toEqual({ replay_id: "r1", page: "2", bar_page: "3" });
    expect(buildReplayQuery(parseReplayQuery({}))).toEqual({});
  });

  it("keeps the prefill only while the create drawer is open", () => {
    const open = parseReplayQuery({ create: "1", strategy_id: "s1", view_id: "v1" });
    expect(buildReplayQuery(open)).toEqual({ create: "1", strategy_id: "s1", view_id: "v1" });
    expect(buildReplayQuery({ ...open, create: false })).toEqual({});
  });

  it("does not write bar_page without a selected replay", () => {
    expect(buildReplayQuery({ replayId: "", page: 1, barPage: 4, create: false, prefill: emptyPrefill })).toEqual({});
  });
});

import { describe, expect, it } from "vitest";
import { detailLink, instancesLink, normalizeTaskTab, replayLink, resultsLink } from "./links";

describe("strategy links", () => {
  it("normalizes the workbench tab with instances as the default", () => {
    expect(normalizeTaskTab("results")).toBe("results");
    expect(normalizeTaskTab(["replay"])).toBe("replay");
    expect(normalizeTaskTab("x")).toBe("instances");
    expect(normalizeTaskTab(undefined)).toBe("instances");
  });

  it("builds instance links without the default tab", () => {
    expect(instancesLink()).toEqual({ name: "strategy-tasks", query: {} });
    expect(instancesLink({ strategyId: "s1", create: true })).toEqual({
      name: "strategy-tasks",
      query: { strategy_id: "s1", create: "1" }
    });
  });

  it("builds result links", () => {
    expect(resultsLink("i1")).toEqual({ name: "strategy-tasks", query: { tab: "results", instance: "i1" } });
    expect(resultsLink()).toEqual({ name: "strategy-tasks", query: { tab: "results" } });
  });

  it("opens the create drawer only when a replay source is given, preferring the instance", () => {
    expect(replayLink({ replayId: "r1" }).query).toEqual({ tab: "replay", replay_id: "r1" });
    expect(replayLink({ instanceId: "i1", strategyId: "s1", viewId: "v" }).query).toEqual({
      tab: "replay",
      create: "1",
      instance_id: "i1",
      view_id: "v"
    });
    expect(replayLink({ strategyId: "s1" }).query).toEqual({ tab: "replay", create: "1", strategy_id: "s1" });
  });

  it("builds the detail link", () => {
    expect(detailLink("i1")).toEqual({ name: "strategy-detail", params: { instanceId: "i1" } });
  });
});

import { describe, expect, it } from "vitest";
import {
  isCrossMarketSelection,
  normalizeMarket,
  planTaskGroups,
  splitTaskName,
  taskTagSelectionError,
  type TaskTagRoute
} from "./collection-task-params";

const spotTag: TaskTagRoute = { tag_id: "tag_spot", source: "binance", market: "spot" };
const swapTag: TaskTagRoute = { tag_id: "tag_swap", source: "binance", market: "swap" };

describe("unified dataset task creation", () => {
  it("detects a selection that spans spot and swap", () => {
    expect(isCrossMarketSelection([spotTag])).toBe(false);
    expect(isCrossMarketSelection([spotTag, { ...spotTag, tag_id: "tag_spot_2" }])).toBe(false);
    expect(isCrossMarketSelection([spotTag, swapTag])).toBe(true);
  });

  it("rejects tags of different sources and markets other than spot plus swap", () => {
    expect(taskTagSelectionError([spotTag, swapTag])).toBe("");
    expect(taskTagSelectionError([spotTag, { ...swapTag, source: "okx" }])).toContain("同一来源");
    expect(taskTagSelectionError([spotTag, swapTag, { ...spotTag, tag_id: "e", market: "equity" }])).toContain("现货和永续");
  });

  it("keeps one task when merged or not crossing markets", () => {
    expect(planTaskGroups([spotTag, swapTag], "unified")).toEqual([{ suffix: "", tagIds: ["tag_spot", "tag_swap"] }]);
    expect(planTaskGroups([spotTag], "split")).toEqual([{ suffix: "", tagIds: ["tag_spot"] }]);
  });

  it("splits by market with spot first", () => {
    expect(planTaskGroups([swapTag, spotTag], "split")).toEqual([
      { suffix: "现货", tagIds: ["tag_spot"] },
      { suffix: "永续", tagIds: ["tag_swap"] }
    ]);
    expect(splitTaskName(" 币安 1 分钟 ", "永续")).toBe("币安 1 分钟（永续）");
    expect(splitTaskName("币安", "")).toBe("币安");
  });

  it("accepts multi as the market of a unified dataset", () => {
    expect(normalizeMarket("multi")).toBe("multi");
  });
});

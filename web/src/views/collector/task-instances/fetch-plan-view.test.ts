import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { backfillCancelable, backfillProgress, functionStateInfo, jobStatusInfo, shortHash, toRFC3339 } from "./fetch-plan-view";

describe("collector instances page helpers", () => {
  it("labels function and job states", () => {
    expect(functionStateInfo("pending")).toEqual({ text: "下发中", color: "orangered" });
    expect(functionStateInfo("applied").text).toBe("已生效");
    expect(functionStateInfo("custom").text).toBe("custom");
    expect(jobStatusInfo("failed").color).toBe("red");
    expect(jobStatusInfo(undefined).text).toBe("未知");
  });

  it("shortens hashes and computes progress from chunk counts", () => {
    expect(shortHash("0123456789abcdef")).toBe("01234567");
    expect(shortHash("")).toBe("-");
    expect(backfillProgress({ pending: 2, running: 1, succeeded: 5, failed: 1, canceled: 1 })).toBe(70);
    expect(backfillProgress({})).toBe(0);
    expect(backfillCancelable({ status: "running" })).toBe(true);
    expect(backfillCancelable({ status: "succeeded" })).toBe(false);
  });

  it("parses RFC3339 and plain UTC time input", () => {
    expect(toRFC3339("2026-10-12T08:00:00Z")).toBe("2026-10-12T08:00:00Z");
    expect(toRFC3339("2026-10-12 08:00")).toBe("2026-10-12T08:00:00Z");
    expect(toRFC3339("")).toBeNull();
    expect(toRFC3339("not-a-time")).toBeNull();
  });
});

describe("collector instances page layout", () => {
  const source = readFileSync(resolve(__dirname, "task-instances.vue"), "utf8");

  it("has the three blocks and no period list", () => {
    expect(source).toContain("<h3>分配</h3>");
    expect(source).toContain("<h3>标的变更日志</h3>");
    expect(source).toContain("<h3>补采</h3>");
    expect(source).toContain('category="collector.subject_change"');
    expect(source).not.toContain("GetTaskInstanceList");
  });

  it("reads the fetch plan and backfill jobs of the selected space", () => {
    expect(source).toContain("getFetchPlans(spaceId)");
    expect(source).toContain("listKlineBackfills(spaceId");
  });
});

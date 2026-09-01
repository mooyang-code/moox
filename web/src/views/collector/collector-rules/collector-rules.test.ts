import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("collector resample rule workbench", () => {
  it("keeps resample configuration and backfill in the existing rule surface", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "collector-rules.vue"), "utf8");
    const backfill = fs.readFileSync(path.resolve(__dirname, "resample-backfill.vue"), "utf8");
    expect(source).toContain("kline_resample");
    expect(source).toContain("ResampleBackfillDialog");
    expect(backfill).toContain("开始回填");
    expect(backfill).toContain("内部行情 `crypto`");
    expect(source).toContain("sourceKeepDuration");
  });
});

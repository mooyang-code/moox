import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { FactorMember, FactorSetInfo, RecalcJob } from "@/api/factor/types";
import {
  defaultRange,
  enabledMemberIds,
  estimateRecalc,
  jobDisplay,
  jobSource,
  progressPercent,
  quickRange,
  segmentStatuses
} from "./recalc-model";

const job = (patch: Partial<RecalcJob> = {}): RecalcJob => ({
  job_id: "job-1",
  request_id: "factor-recalc-1",
  set_id: "set_a",
  factor_ids: ["Bias"],
  subjects: [],
  start_time: "2026-10-01T00:00:00Z",
  end_time: "2026-10-03T00:00:00Z",
  status: "running",
  progress_time: "2026-10-02T00:00:00Z",
  error: "",
  created_at: "",
  updated_at: "",
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

describe("recalc model", () => {
  it("status segment filters (all / running / succeeded / failed+cancelled)", () => {
    expect(segmentStatuses("all")).toBeUndefined();
    expect(segmentStatuses("running")).toEqual(["accepted", "running"]);
    expect(segmentStatuses("succeeded")).toEqual(["succeeded"]);
    expect(segmentStatuses("failed")).toEqual(["failed", "cancelled"]);
  });

  it("source tag: factor-enable- prefix means 启用回填", () => {
    expect(jobSource(job({ request_id: "factor-enable-Bias-123" }))).toEqual({ kind: "enable", label: "启用回填" });
    expect(jobSource(job({ request_id: "factor-recalc-9" }))).toEqual({ kind: "manual", label: "手动" });
  });

  it("degraded note renders as 部分降级 not error", () => {
    const degraded = jobDisplay(job({ status: "succeeded", error: "degraded: 3 chunks skipped" }));
    expect(degraded).toMatchObject({ degraded: true, degradedText: "3 chunks skipped", errorText: "" });
    expect(degraded.statusLabel).toBe("已完成");
    const failed = jobDisplay(job({ status: "failed", error: "python worker down" }));
    expect(failed).toMatchObject({ degraded: false, errorText: "python worker down" });
    expect(jobDisplay(job({ status: "succeeded" }))).toMatchObject({ degraded: false, errorText: "" });
  });

  it("progress percent from progress_time within range", () => {
    expect(progressPercent(job())).toBe(50);
    expect(progressPercent(job({ status: "succeeded" }))).toBe(100);
    expect(progressPercent(job({ progress_time: "" }))).toBe(0);
  });

  it("estimate periods x factors", () => {
    const hour = 3_600_000;
    const start = Date.UTC(2026, 9, 1);
    const result = estimateRecalc({
      freq: "1h",
      start,
      end: start + 24 * hour,
      factorCount: 3,
      subjectMode: "all",
      subjectCount: 0
    });
    expect(result).toMatchObject({ periods: 24, factors: 3 });
    expect(result.text).toBe("将处理 24 个周期 × 3 个因子 × 全部对象");
    const scoped = estimateRecalc({
      freq: "1h",
      start,
      end: start + 2 * hour,
      factorCount: 1,
      subjectMode: "include",
      subjectCount: 4
    });
    expect(scoped.text).toBe("将处理 2 个周期 × 1 个因子 × 4 个对象");
    expect(
      estimateRecalc({ freq: "?", start, end: start + hour, factorCount: 1, subjectMode: "all", subjectCount: 0 }).periods
    ).toBe(0);
    expect(estimateRecalc({ freq: "1h", start, end: start, factorCount: 1, subjectMode: "all", subjectCount: 0 }).periods).toBe(
      0
    );
  });

  it("default factor selection is all enabled members", () => {
    const info: FactorSetInfo = {
      factor_set: {} as FactorSetInfo["factor_set"],
      members: [member("A", "enabled"), member("B", "disabled"), member("C", "enabled")]
    };
    expect(enabledMemberIds(info)).toEqual(["A", "C"]);
  });

  it("default and quick ranges align to the frequency", () => {
    const now = Date.UTC(2026, 9, 5, 10, 17, 30);
    const hour = 3_600_000;
    expect(defaultRange("1h", now)).toEqual({ end: Date.UTC(2026, 9, 5, 10), start: Date.UTC(2026, 9, 5, 10) - 100 * hour });
    expect(quickRange("1h", "day", now)).toEqual({ end: Date.UTC(2026, 9, 5, 10), start: Date.UTC(2026, 9, 4, 10) });
    expect(quickRange("1h", "week", now)).toEqual({ end: Date.UTC(2026, 9, 5, 10), start: Date.UTC(2026, 8, 28, 10) });
    expect(defaultRange("bad", now)).toBeNull();
  });
});

describe("recalc page contract", () => {
  const page = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");

  it("recalc tab has no explanation row, uses info-tip", () => {
    expect(page).toContain("InfoTip");
    expect(page).not.toContain("recalc-tip");
    expect(page).not.toContain('<a-alert type="info"');
  });

  it("uses listRecalcJobs and cancelRecalcJob, not getRecalcJob", () => {
    expect(page).toContain("listRecalcJobs");
    expect(page).toContain("cancelRecalcJob");
    expect(page).not.toContain("getRecalcJob");
  });

  it("uses splitJobNote semantics for degraded jobs", () => {
    expect(page).toContain("部分降级");
  });
});

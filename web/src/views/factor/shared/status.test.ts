import { describe, expect, it } from "vitest";
import { isActiveJob, jobProgress, jobStatusTag, taskStatusTag, splitJobNote } from "./status";

describe("splitJobNote", () => {
  it("treats the degraded prefix as a note, not an error", () => {
    expect(splitJobNote({ status: "succeeded", error: "degraded: chunk 3 skipped" })).toEqual({
      degraded: "chunk 3 skipped",
      error: ""
    });
    expect(splitJobNote({ status: "failed", error: "storage unavailable" })).toEqual({
      degraded: "",
      error: "storage unavailable"
    });
    expect(splitJobNote({ status: "succeeded", error: "" })).toEqual({ degraded: "", error: "" });
  });
});

describe("job helpers", () => {
  it("computes progress inside the window and caps at the bounds", () => {
    const job = {
      status: "running" as const,
      start_time: "2026-01-01T00:00:00Z",
      end_time: "2026-01-01T01:00:00Z",
      progress_time: "2026-01-01T00:30:00Z"
    };
    expect(jobProgress(job)).toBe(50);
    expect(jobProgress({ ...job, status: "succeeded" })).toBe(100);
    expect(jobProgress({ ...job, progress_time: "" })).toBe(0);
  });

  it("only accepted and running jobs are active", () => {
    expect(isActiveJob({ status: "accepted" })).toBe(true);
    expect(isActiveJob({ status: "running" })).toBe(true);
    expect(isActiveJob({ status: "succeeded" })).toBe(false);
  });

  it("maps statuses to labels and falls back for unknown values", () => {
    expect(taskStatusTag("pending").label).toBe("创建中");
    expect(jobStatusTag("cancelled").label).toBe("已取消");
    expect(taskStatusTag("???")).toEqual({ label: "???", color: "gray" });
  });
});

import { describe, expect, it } from "vitest";
import { rangeToTime } from "./trade-record-utils";

describe("trade record filters", () => {
  it("includes the complete end date in a date range", () => {
    expect(rangeToTime([1_700_000_000_000, 1_700_086_400_000])).toEqual({
      start_time: "1700000000000",
      end_time: "1700172799999"
    });
  });

  it("omits incomplete date ranges", () => {
    expect(rangeToTime([])).toEqual({});
    expect(rangeToTime([1_700_000_000_000])).toEqual({});
  });
});

import { describe, expect, it } from "vitest";
import { nextRuns, toTagPayload, validateTagForm, type TagFormState } from "./tag-form";

function form(overrides: Partial<TagFormState> = {}): TagFormState {
  return {
    tag_id: "crypto_spot",
    tag_name: "加密现货",
    description: "",
    mode: "manual",
    probe: false,
    sources: [],
    instrument_type: "",
    cron: "0 * * * *",
    timezone: "UTC",
    ...overrides
  };
}

describe("subject tag form", () => {
  it("validates a new tag id but does not revalidate an existing id", () => {
    expect(validateTagForm(form({ tag_id: "Bad-ID" }), true)).toBe("标签 ID 须为小写字母开头的 snake_case");
    expect(validateTagForm(form({ tag_id: "Bad-ID" }), false)).toBeUndefined();
  });

  it("clears probe settings for a manual tag without automatic probing", () => {
    expect(
      toTagPayload(
        "crypto",
        form({ sources: ["binance"], instrument_type: "spot", cron: "*/5 * * * *", timezone: "Asia/Shanghai" })
      )
    ).toMatchObject({ sources: [], instrument_type: "", cron: "*/5 * * * *", timezone: "Asia/Shanghai" });
  });

  it("returns the next scheduled runs in the selected timezone", () => {
    const runs = nextRuns("0 * * * *", "UTC", 3);
    expect(runs).toHaveLength(3);
    expect(runs.every(value => value.endsWith(":00.000Z"))).toBe(true);
  });
});

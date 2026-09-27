import { describe, expect, it } from "vitest";
import { nextRuns, toTagPayload, validateTagForm, type TagFormState } from "./tag-form";

function form(overrides: Partial<TagFormState> = {}): TagFormState {
  return {
    tag_id: "crypto_spot",
    tag_name: "加密现货",
    description: "",
    mode: "manual",
    source: "binance",
    market_type: "spot",
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

  it("requires one source and market type for manual and auto tags", () => {
    expect(validateTagForm(form({ source: "" }), true)).toBe("请选择数据源与市场类型");
    expect(validateTagForm(form({ market_type: "" }), true)).toBe("请选择数据源与市场类型");
  });

  it("persists the single authoritative source and market type", () => {
    expect(toTagPayload("crypto", form())).toMatchObject({ source: "binance", market_type: "spot" });
  });

  it("returns the next scheduled runs in the selected timezone", () => {
    const runs = nextRuns("0 * * * *", "UTC", 3);
    expect(runs).toHaveLength(3);
    expect(runs.every(value => value.endsWith(":00.000Z"))).toBe(true);
  });
});

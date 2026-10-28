import { describe, expect, it } from "vitest";
import type { Tag } from "@/api/storage/types";
import { nextRuns, tagToFormState, toTagPayload, validateTagForm, type TagFormState } from "./tag-form";

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

  it("writes source and market type into attributes, not top-level fields", () => {
    const payload = toTagPayload("crypto", form());
    expect(payload.attributes).toEqual({ "sync.source": "binance", market_type: "spot" });
    expect(payload).not.toHaveProperty("source");
    expect(payload).not.toHaveProperty("market_type");
  });

  it("writes cron and timezone only for auto tags, defaulting to hourly UTC", () => {
    expect(toTagPayload("crypto", form({ mode: "auto", cron: " 5 * * * * ", timezone: "Asia/Shanghai" })).attributes).toEqual({
      "sync.source": "binance",
      market_type: "spot",
      "sync.cron": "5 * * * *",
      "sync.timezone": "Asia/Shanghai"
    });
    expect(toTagPayload("crypto", form({ mode: "auto", cron: "", timezone: "" })).attributes).toMatchObject({
      "sync.cron": "0 * * * *",
      "sync.timezone": "UTC"
    });
  });

  it("keeps unrelated attributes on edit and drops stale schedule keys when switching to manual", () => {
    const existing: Tag = {
      space_id: "crypto",
      tag_id: "crypto_spot",
      tag_name: "加密现货",
      mode: "auto",
      attributes: { "sync.source": "okx", market_type: "swap", "sync.cron": "*/5 * * * *", "sync.timezone": "UTC", owner: "ops" }
    };
    expect(toTagPayload("crypto", form({ mode: "manual" }), existing).attributes).toEqual({
      "sync.source": "binance",
      market_type: "spot",
      owner: "ops"
    });
  });

  it("reads the form state back from attributes with schedule defaults", () => {
    const tag: Tag = {
      space_id: "crypto",
      tag_id: "crypto_spot",
      tag_name: "加密现货",
      mode: "manual",
      attributes: { "sync.source": "binance", market_type: "spot" }
    };
    expect(tagToFormState(tag)).toMatchObject({ source: "binance", market_type: "spot", cron: "0 * * * *", timezone: "UTC" });
    expect(tagToFormState({ ...tag, attributes: undefined })).toMatchObject({ source: "", market_type: "" });
  });

  it("returns the next scheduled runs in the selected timezone", () => {
    const runs = nextRuns("0 * * * *", "UTC", 3);
    expect(runs).toHaveLength(3);
    expect(runs.every(value => value.endsWith(":00.000Z"))).toBe(true);
  });
});

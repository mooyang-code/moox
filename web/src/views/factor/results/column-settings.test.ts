import { afterEach, describe, expect, it, vi } from "vitest";
import type { FactorScope } from "./factor-scope";
import {
  MAX_PINNED,
  defaultColumnSettings,
  loadColumnSettings,
  normalizeColumnSettings,
  saveColumnSettings,
  tableColumns
} from "./column-settings";

function scopeWith(outputs: string[], baseMarket = ["open", "close"], otherSource = ["request_id"]): FactorScope {
  const items = outputs.map(column => ({
    column,
    output: column,
    alias: column,
    factorId: column,
    factorName: column,
    categoryId: "c"
  }));
  return {
    outputs: items,
    outputByColumn: new Map(items.map(item => [item.column, item])),
    paramsByAlias: new Map(),
    baseMarket,
    otherSource,
    categoryNames: new Map(),
    categoryOrder: ["c"]
  };
}

describe("列设置", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("结构错误或不是对象时回到默认", () => {
    const scope = scopeWith(["rsi"]);
    expect(normalizeColumnSettings(null, scope)).toEqual(defaultColumnSettings(scope));
    expect(normalizeColumnSettings({ hiddenSystem: [], market: "open", pinned: [] }, scope)).toEqual(
      defaultColumnSettings(scope)
    );
    expect(normalizeColumnSettings({ hiddenSystem: [], market: [1], pinned: [] }, scope)).toEqual(defaultColumnSettings(scope));
  });

  it("去重、不可隐藏项强制显示、剔除失效列、钉住列有上限", () => {
    const outputs = Array.from({ length: 40 }, (_, index) => `f${index}`);
    const scope = scopeWith(outputs);
    const normalized = normalizeColumnSettings(
      {
        hiddenSystem: ["freq", "subject_id", "data_time", "series_tag", "bogus"],
        market: ["close", "close", "request_id", "gone"],
        pinned: ["gone", ...outputs, "f0"]
      },
      scope
    );
    expect(normalized.hiddenSystem).toEqual(["freq", "series_tag"]);
    expect(normalized.market).toEqual(["close", "request_id"]);
    expect(normalized.pinned).toHaveLength(MAX_PINNED);
    expect(normalized.pinned[0]).toBe("f0");
  });

  it("按因子集分别读写本地存储；存储不可用或 JSON 无效时回到默认", () => {
    const scope = scopeWith(["rsi"]);
    saveColumnSettings("set-a", { hiddenSystem: ["freq"], market: ["close"], pinned: ["rsi"] });
    expect(loadColumnSettings("set-a", scope)).toEqual({ hiddenSystem: ["freq"], market: ["close"], pinned: ["rsi"] });
    expect(loadColumnSettings("set-b", scope)).toEqual(defaultColumnSettings(scope));

    window.localStorage.setItem("moox.factor-results.columns.set-c", "{oops");
    expect(loadColumnSettings("set-c", scope)).toEqual(defaultColumnSettings(scope));

    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(loadColumnSettings("set-a", scope)).toEqual(defaultColumnSettings(scope));
    expect(() => saveColumnSettings("set-a", defaultColumnSettings(scope))).not.toThrow();
  });

  it("隐藏全部行情列且没有钉住列时，投影一个基础行情列但不显示", () => {
    const scope = scopeWith(["rsi"]);
    const table = tableColumns({ hiddenSystem: ["freq"], market: [], pinned: [] }, scope);
    expect(table.display).toEqual({ system: ["subject_id", "series_tag", "data_time"], business: [] });
    expect(table.project).toEqual(["close"]);
    expect(table.search).toEqual([]);
  });
});

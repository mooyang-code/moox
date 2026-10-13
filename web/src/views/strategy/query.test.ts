import { describe, expect, it } from "vitest";
import { buildQuery, queryEnum, queryPage, queryString, sameQuery } from "./query";

describe("strategy query helpers", () => {
  it("reads the first trimmed value", () => {
    expect(queryString([" a ", "b"])).toBe("a");
    expect(queryString(undefined)).toBe("");
    expect(queryString(null)).toBe("");
  });

  it("normalizes page numbers", () => {
    expect(queryPage("3")).toBe(3);
    expect(queryPage("0")).toBe(1);
    expect(queryPage("-2")).toBe(1);
    expect(queryPage("1.5")).toBe(1);
    expect(queryPage("abc")).toBe(1);
    expect(queryPage(undefined)).toBe(1);
  });

  it("falls back for values outside the allowed set", () => {
    expect(queryEnum("true", ["true", "false"] as const, "" as "true" | "false" | "")).toBe("true");
    expect(queryEnum("x", ["true", "false"] as const, "" as "true" | "false" | "")).toBe("");
  });

  it("builds queries without empty values or defaults", () => {
    expect(buildQuery({ page: 1, strategy_id: "s1", enabled: "", create: undefined }, { page: 1 })).toEqual({ strategy_id: "s1" });
    expect(buildQuery({ page: 2, create: true })).toEqual({ create: "true", page: "2" });
  });

  it("compares queries by meaning", () => {
    expect(sameQuery({ a: "1", b: "" }, { a: ["1", "2"] })).toBe(true);
    expect(sameQuery({ a: "1" }, { a: "2" })).toBe(false);
  });
});

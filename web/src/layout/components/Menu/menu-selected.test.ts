import { describe, expect, it } from "vitest";
import { resolveSelectedKeys } from "./menu-selected";

describe("resolveSelectedKeys", () => {
  it("prefers meta.activeMenu so hidden pages highlight their owning menu", () => {
    expect(resolveSelectedKeys({ name: "factor-definition-new", meta: { activeMenu: "factor-definitions" } })).toEqual([
      "factor-definitions"
    ]);
  });

  it("falls back to the route name", () => {
    expect(resolveSelectedKeys({ name: "factor-overview", meta: {} })).toEqual(["factor-overview"]);
    expect(resolveSelectedKeys({ name: "factor-overview" })).toEqual(["factor-overview"]);
  });

  it("returns no key for an unnamed route", () => {
    expect(resolveSelectedKeys({ meta: {} })).toEqual([]);
  });
});

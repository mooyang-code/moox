import { describe, expect, it } from "vitest";
import { resolveOpenKeys, resolveSelectedKeys } from "./menu-selected";

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

describe("resolveOpenKeys", () => {
  const tree = [
    { name: "home", children: null },
    {
      name: "ops",
      children: [
        { name: "ops-monitor" },
        { name: "ops-logs", children: [{ name: "ops-logs-collector" }, { name: "ops-logs-factor" }] }
      ]
    },
    { name: "settings", children: [{ name: "settings-secrets" }] }
  ];

  it("opens every parent of a nested page, outermost first", () => {
    expect(resolveOpenKeys(tree, ["ops-logs-factor"])).toEqual(["ops", "ops-logs"]);
    expect(resolveOpenKeys(tree, ["settings-secrets"])).toEqual(["settings"]);
  });

  it("opens nothing for a top-level page, an unknown key or an empty tree", () => {
    expect(resolveOpenKeys(tree, ["home"])).toEqual([]);
    expect(resolveOpenKeys(tree, ["missing"])).toEqual([]);
    expect(resolveOpenKeys(undefined, ["ops-monitor"])).toEqual([]);
  });
});

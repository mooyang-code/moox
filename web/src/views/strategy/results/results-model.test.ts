import { describe, expect, it } from "vitest";
import type { StrategyInstance } from "@/api/strategy-types";
import { instanceTabTitle, instanceTabs, resolveSelectedInstance, sortInstancesForResults } from "./results-model";

function instance(id: string, overrides: Partial<StrategyInstance> = {}): StrategyInstance {
  return {
    instance_id: id,
    strategy_id: "s1",
    space_id: "crypto",
    view_id: "v",
    logical_account_id: "",
    enabled: true,
    session_id: "sess",
    resolved_json: "{}",
    health: "ok",
    created_at: "",
    updated_at: "2026-10-01T00:00:00Z",
    ...overrides
  };
}
const catalog = [{ strategy_id: "s1", name: "动量", dsl_yaml: "", dsl_hash: "", created_at: "", updated_at: "" }];

describe("strategy results model", () => {
  it("orders enabled first, then instances needing attention, then the most recently updated", () => {
    const ordered = sortInstancesForResults([
      instance("off", { enabled: false, updated_at: "2026-10-09T00:00:00Z" }),
      instance("old", { updated_at: "2026-10-01T00:00:00Z" }),
      instance("new", { updated_at: "2026-10-05T00:00:00Z" }),
      instance("bad", { health: "degraded", updated_at: "2026-09-01T00:00:00Z" })
    ]);
    expect(ordered.map(item => item.instance_id)).toEqual(["bad", "new", "old", "off"]);
  });

  it("titles a tab with the definition name and a short instance id", () => {
    expect(instanceTabTitle(instance("26d51f7e-4c40-4e33"), catalog)).toBe("动量 · 26d51f7e");
    expect(instanceTabTitle(instance("i2", { enabled: false, strategy_id: "gone" }), catalog)).toBe("gone · i2（停用）");
  });

  it("prefers the requested instance and falls back to the first tab", () => {
    const tabs = instanceTabs([instance("a"), instance("b", { enabled: false })], catalog);
    expect(resolveSelectedInstance("zzz", tabs)).toBe("zzz");
    expect(resolveSelectedInstance("", tabs)).toBe("a");
    expect(resolveSelectedInstance("", [])).toBe("");
  });
});

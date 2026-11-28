import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { spacePolicyForUrl } from "@/api/admin/space-client";
import { flatRoutes } from "@/router/route";

function read(path: string) {
  return readFileSync(resolve(__dirname, `../src/views/${path}`), "utf8");
}

describe("space-scoped request ownership", () => {
  it("rebuilds home as a space-scoped route and invalidates in-flight loads on unmount", () => {
    const homeRoute = flatRoutes().find(route => route.name === "home");
    expect(homeRoute?.meta.scope).toBe("space");

    const source = read("home/home.vue");
    expect(source).toContain("const spaceLoadGate = createLatestRequestGuard()");
    expect(source).toContain("const token = spaceLoadGate.begin()");
    expect(source).toContain("spaceLoadGate.invalidate()");
    const ruleCall = source.indexOf('"GetTaskList"');
    expect(source.slice(ruleCall, ruleCall + 180)).toContain("{ space_id: spaceId, page: { page: 1, size: 1 } }");
  });

  it("keeps blocked instruments and strategy definitions working without a selected space or across space switches", () => {
    expect(spacePolicyForUrl("/api/admin/trade/ListBlockedInstruments")).toBe("global");
    expect(spacePolicyForUrl("/api/admin/trade/UnblockInstrument")).toBe("global");
    expect(spacePolicyForUrl("/api/admin/trade/ListDelists")).toBe("global");
    expect(spacePolicyForUrl("/api/admin/strategy/ListStrategies")).toBe("global");
    expect(spacePolicyForUrl("/api/admin/strategy/CreateStrategy")).toBe("global");
    expect(spacePolicyForUrl("/api/admin/strategy/UpdateStrategy")).toBe("global");
    expect(spacePolicyForUrl("/api/admin/strategy/DeleteStrategy")).toBe("global");
    expect(spacePolicyForUrl("/api/admin/strategy/GetStrategy")).toBe("space_optional");
    expect(spacePolicyForUrl("/api/admin/strategy/ValidateStrategy")).toBe("space_optional");
    expect(spacePolicyForUrl("/api/admin/activity/ListActivityLogs")).toBe("space_optional");
    expect(spacePolicyForUrl("/api/admin/factor-mgr/ListFactorTasks")).toBe("space_optional");
    expect(spacePolicyForUrl("/api/admin/trade/ListOrders")).toBe("space_required");
    expect(spacePolicyForUrl("/api/admin/strategy/ListStrategyInstances")).toBe("space_required");

    // 策略定义页属于全局视图，不因无选中空间而阻止加载
    const definitionsPage = read("strategy/definitions/index.vue");
    expect(definitionsPage).not.toContain("if (spaceStore.selectedSpaceId) void refresh()");
  });
});

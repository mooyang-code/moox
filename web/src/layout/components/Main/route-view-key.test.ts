import { describe, expect, it } from "vitest";
import { routeViewKey, spaceSwitchNavigation } from "./route-view-key";

describe("routeViewKey", () => {
  it("全局页与未声明 scope 的路由只按 path 区分：切空间代次不影响 key", () => {
    expect(routeViewKey({ path: "/ops/hosts", meta: { scope: "global" } }, 1)).toBe("/ops/hosts");
    expect(routeViewKey({ path: "/ops/hosts", meta: { scope: "global" } }, 2)).toBe("/ops/hosts");
    expect(routeViewKey({ path: "/other", meta: {} }, 3)).toBe("/other");
  });

  it("空间内页面附带空间代次：同空间内 query 变化复用，切空间代次变化时重建", () => {
    const route = { path: "/strategy/tasks", meta: { scope: "space" } };
    expect(routeViewKey(route, 1)).toBe("/strategy/tasks@space:1");
    expect(routeViewKey(route, 2)).toBe("/strategy/tasks@space:2");
  });

  it("声明了 viewKey 的几条地址算同一个页面：key 看 viewKey、路由参数与空间代次", () => {
    const meta = { viewKey: "trading-account-detail", scope: "space" };
    const generic = routeViewKey({ path: "/trading/accounts/a1", params: { id: "a1" }, meta }, 1);
    const paper = routeViewKey({ path: "/trading/paper/a1", params: { id: "a1" }, meta }, 1);
    expect(generic).toBe("trading-account-detail/a1@space:1");
    expect(paper).toBe(generic);
    expect(routeViewKey({ path: "/trading/paper/a2", params: { id: "a2" }, meta }, 1)).not.toBe(generic);
    expect(routeViewKey({ path: "/trading/paper/a1", params: { id: "a1" }, meta }, 2)).toBe("trading-account-detail/a1@space:2");
  });
});

describe("spaceSwitchNavigation", () => {
  it("全局页切空间不导航", () => {
    expect(spaceSwitchNavigation({ path: "/ops/logs", query: { tab: "important" }, meta: { scope: "global" } })).toBeNull();
  });

  it("带 spaceRedirect 的空间内详情页切空间跳回列表", () => {
    expect(
      spaceSwitchNavigation({
        path: "/strategy/detail/i1",
        query: { tab: "results" },
        meta: { scope: "space", spaceRedirect: "/strategy/tasks" }
      })
    ).toEqual({ path: "/strategy/tasks" });
  });

  it("带查询参数的空间内列表页切空间清空 query；无 query 时无需导航", () => {
    expect(
      spaceSwitchNavigation({
        path: "/strategy/tasks",
        query: { tab: "replay", replay_id: "r1" },
        meta: { scope: "space" }
      })
    ).toEqual({ path: "/strategy/tasks", query: {} });
    expect(spaceSwitchNavigation({ path: "/home", query: {}, meta: { scope: "space" } })).toBeNull();
  });
});

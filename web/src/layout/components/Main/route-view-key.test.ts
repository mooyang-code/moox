import { describe, expect, it } from "vitest";
import { routeViewKey } from "./route-view-key";

describe("routeViewKey", () => {
  it("只按 path 区分：query 变化时复用组件", () => {
    expect(routeViewKey({ path: "/strategy/running" })).toBe("/strategy/running");
    expect(routeViewKey({ path: "/other", meta: {} })).toBe("/other");
  });

  it("声明了 viewKey 的几条地址算同一个页面：key 只看 viewKey 和路由参数", () => {
    const meta = { viewKey: "trading-account-detail" };
    const generic = routeViewKey({ path: "/trading/accounts/a1", params: { id: "a1" }, meta });
    const paper = routeViewKey({ path: "/trading/paper/a1", params: { id: "a1" }, meta });
    expect(generic).toBe("trading-account-detail/a1");
    expect(paper).toBe(generic);
    expect(routeViewKey({ path: "/trading/paper/a2", params: { id: "a2" }, meta })).not.toBe(generic);
  });
});

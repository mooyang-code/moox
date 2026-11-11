import { describe, expect, it } from "vitest";
import { routeViewKey } from "./route-view-key";

describe("routeViewKey", () => {
  it("keeps the component across query changes only for reuseOnQuery routes", () => {
    const reuse = { path: "/strategy/running", meta: { reuseOnQuery: true } };
    expect(routeViewKey({ ...reuse, fullPath: "/strategy/running?page=2" })).toBe("/strategy/running");
    expect(routeViewKey({ path: "/other", fullPath: "/other?tab=a", meta: {} })).toBe("/other?tab=a");
    expect(routeViewKey({ path: "/other", fullPath: "/other" })).toBe("/other");
  });

  it("声明了 viewKey 的几条地址算同一个页面：key 只看 viewKey 和路由参数", () => {
    const meta = { viewKey: "trading-account-detail", reuseOnQuery: true };
    const generic = routeViewKey({ path: "/trading/accounts/a1", fullPath: "/trading/accounts/a1", params: { id: "a1" }, meta });
    const paper = routeViewKey({
      path: "/trading/paper/a1",
      fullPath: "/trading/paper/a1?tab=orders",
      params: { id: "a1" },
      meta
    });
    expect(generic).toBe("trading-account-detail/a1");
    expect(paper).toBe(generic);
    expect(routeViewKey({ path: "/trading/paper/a2", fullPath: "/trading/paper/a2", params: { id: "a2" }, meta })).not.toBe(
      generic
    );
  });
});

import { describe, expect, it } from "vitest";
import { routeViewKey } from "./route-view-key";

describe("routeViewKey", () => {
  it("keeps the component across query changes only for reuseOnQuery routes", () => {
    const reuse = { path: "/strategy/running", meta: { reuseOnQuery: true } };
    expect(routeViewKey({ ...reuse, fullPath: "/strategy/running?page=2" })).toBe("/strategy/running");
    expect(routeViewKey({ path: "/other", fullPath: "/other?tab=a", meta: {} })).toBe("/other?tab=a");
    expect(routeViewKey({ path: "/other", fullPath: "/other" })).toBe("/other");
  });
});

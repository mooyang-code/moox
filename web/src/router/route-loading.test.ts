import { describe, expect, it } from "vitest";
import { shouldShowRouteLoading } from "./route-loading";

describe("shouldShowRouteLoading", () => {
  const reuse = { path: "/strategy/running", meta: { reuseOnQuery: true } };
  it("skips the global loading for same-path query navigation on reuse routes", () => {
    expect(shouldShowRouteLoading(reuse, reuse)).toBe(false);
  });
  it("shows it for other navigations", () => {
    expect(shouldShowRouteLoading(reuse, { path: "/strategy/replay" })).toBe(true);
    expect(shouldShowRouteLoading(reuse, undefined)).toBe(true);
    expect(shouldShowRouteLoading({ path: "/a", meta: {} }, { path: "/a" })).toBe(true);
  });
});

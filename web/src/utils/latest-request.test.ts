import { describe, expect, it } from "vitest";

import { createLatestRequestGuard } from "./latest-request";

describe("createLatestRequestGuard", () => {
  it("rejects an older request after a newer request starts", () => {
    const guard = createLatestRequestGuard();
    const first = guard.begin();
    const second = guard.begin();

    expect(first.isLatest()).toBe(false);
    expect(second.isLatest()).toBe(true);
  });

  it("can invalidate an in-flight request without starting another", () => {
    const guard = createLatestRequestGuard();
    const request = guard.begin();

    guard.invalidate();

    expect(request.isLatest()).toBe(false);
  });
});

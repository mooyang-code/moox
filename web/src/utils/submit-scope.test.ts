import { describe, expect, it } from "vitest";
import { createSubmitScope } from "./submit-scope";

describe("createSubmitScope", () => {
  it("invalidates in-flight submissions when invalidated on close, reopen, or unmount", () => {
    const scope = createSubmitScope();
    const submission = scope.begin();
    expect(submission.isCurrent()).toBe(true);
    scope.invalidate();
    expect(submission.isCurrent()).toBe(false);
    expect(scope.begin().isCurrent()).toBe(true);
  });
});

import { describe, expect, it } from "vitest";
import { setSelectedSpaceIdCache } from "@/api/admin/space-header";
import { createSubmitScope } from "./submit-scope";

describe("createSubmitScope", () => {
  it("invalidates in-flight submissions when the dialog is closed or reopened", () => {
    const scope = createSubmitScope();
    const submission = scope.begin();
    expect(submission.isCurrent()).toBe(true);
    scope.invalidate();
    expect(submission.isCurrent()).toBe(false);
    expect(scope.begin().isCurrent()).toBe(true);
  });

  it("invalidates space-scoped submissions on Space changes (including A→B→A) but not global ones", () => {
    setSelectedSpaceIdCache("space-a");
    const scoped = createSubmitScope();
    const global = createSubmitScope({ global: true });
    const a = scoped.begin();
    const g = global.begin();
    setSelectedSpaceIdCache("space-b");
    setSelectedSpaceIdCache("space-a");
    expect(a.isCurrent()).toBe(false);
    expect(g.isCurrent()).toBe(true);
    setSelectedSpaceIdCache("");
  });
});

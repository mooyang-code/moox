import { afterEach, describe, expect, it, vi } from "vitest";
import { goBackOr } from "./navigate-back";

afterEach(() => window.history.replaceState(null, ""));

describe("goBackOr", () => {
  it("有站内来源时后退，没有时回到指定列表", () => {
    const router = { back: vi.fn(), push: vi.fn() };
    window.history.replaceState({ back: "/factor/tasks?detail=t1" }, "");
    goBackOr(router as never, "/factor/definitions");
    expect(router.back).toHaveBeenCalled();
    expect(router.push).not.toHaveBeenCalled();

    window.history.replaceState({ back: null }, "");
    goBackOr(router as never, "/factor/definitions");
    expect(router.push).toHaveBeenCalledWith("/factor/definitions");
  });
});

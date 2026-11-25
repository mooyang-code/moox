import { describe, expect, it } from "vitest";

import { statusLabel } from "./metadata-utils";

describe("statusLabel", () => {
  it("localizes enabled and disabled status values without changing API values", () => {
    expect(statusLabel("enabled")).toBe("已启用");
    expect(statusLabel("disabled")).toBe("已停用");
    expect(statusLabel("active")).toBe("已启用");
    expect(statusLabel("inactive")).toBe("已停用");
    expect(statusLabel("deleted")).toBe("已删除");
    expect(statusLabel("building")).toBe("building");
  });
});

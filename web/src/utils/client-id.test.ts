import { describe, expect, it } from "vitest";

import { createClientId } from "./client-id";

describe("createClientId", () => {
  it("creates nonempty unique IDs without randomUUID", () => {
    const first = createClientId();
    const second = createClientId();

    expect(first).toMatch(/^[0-9a-f-]{36}$/);
    expect(second).toMatch(/^[0-9a-f-]{36}$/);
    expect(second).not.toBe(first);
  });
});

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn() }));
vi.mock("@/api/http", () => ({ call: mocks.call }));

import { listSecrets } from "./secret";

describe("secret API pagination", () => {
  beforeEach(() => mocks.call.mockReset());

  it("uses unified common.Page request and PageResult response for ListSecrets", async () => {
    mocks.call.mockResolvedValueOnce({
      secrets: [{ secret_id: "sec-1", name: "Binance Prod", category: "exchange", status: "active" }],
      page_result: { page: 2, size: 20, total: 35, has_more: false, total_state: "EXACT" }
    });

    const rsp = await listSecrets({ category: "exchange", status: "active", page: { page: 2, size: 20 } });

    expect(mocks.call).toHaveBeenCalledWith("secret", "ListSecrets", {
      category: "exchange",
      status: "active",
      page: { page: 2, size: 20 }
    });
    expect(rsp.secrets).toHaveLength(1);
    expect(rsp.page_result).toMatchObject({ page: 2, size: 20, total: 35, has_more: false });
  });
});

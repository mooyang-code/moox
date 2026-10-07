import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  post: vi.fn(),
  requestUse: vi.fn(),
  responseUse: vi.fn()
}));

vi.mock("axios", () => ({
  default: {
    create: vi.fn(() => ({
      post: mocks.post,
      interceptors: {
        request: { use: mocks.requestUse },
        response: { use: mocks.responseUse }
      }
    }))
  }
}));

import { callStorage } from "./http";

describe("Storage HTTP facade", () => {
  beforeEach(() => {
    mocks.post.mockResolvedValue({ data: { ret_info: { code: 0 } } });
    mocks.post.mockClear();
  });

  it("uses the fixed Admin Gateway Storage BFF path", async () => {
    await callStorage("ListDataSources", { space_id: "space-1" });

    expect(mocks.post).toHaveBeenCalledWith(
      "/api/admin/storage/ListDataSources",
      expect.objectContaining({
        auth_info: expect.objectContaining({ app_id: "moox_frontend" }),
        space_id: "space-1"
      })
    );
  });

  it("deduplicates short-lived read requests and invalidates them on writes", async () => {
    const req = { space_id: "cache-space" };
    await callStorage("ListDataSources", req);
    await callStorage("ListDataSources", req);
    expect(mocks.post).toHaveBeenCalledTimes(1);

    await callStorage("UpdateDataSource", { space_id: "cache-space", data_source_id: "source-cache" });
    await callStorage("ListDataSources", req);
    expect(mocks.post).toHaveBeenCalledTimes(3);
  });
});

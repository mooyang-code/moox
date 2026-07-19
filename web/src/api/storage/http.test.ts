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
    await callStorage("GetDataSource", { space_id: "space-1" });

    expect(mocks.post).toHaveBeenCalledWith(
      "/api/admin/storage/GetDataSource",
      expect.objectContaining({
        auth_info: expect.objectContaining({ app_id: "moox_frontend" }),
        space_id: "space-1"
      })
    );
  });
});

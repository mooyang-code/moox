import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn() }));

// 只看 Storage 的方法名、请求体与选项：服务名固定是 storage
vi.mock("@/api/http", () => ({
  call: (_service: string, method: string, req: unknown, options?: unknown) =>
    options === undefined ? mocks.call(method, req) : mocks.call(method, req, options)
}));

import { listDatasets } from "./metadata";

describe("Storage metadata Dataset APIs", () => {
  beforeEach(() => {
    mocks.call.mockReset();
    mocks.call.mockResolvedValue({
      ret_info: { code: 0, msg: "" },
      datasets: [],
      page_result: { page: 1, size: 20, total: 0, has_more: false, next_cursor: "" }
    });
  });

  it("preserves supported Proto JSON Dataset filters", async () => {
    await listDatasets({
      space_id: "space-a",
      data_source_id: "source-a",
      data_kind: "DATA_KIND_TIME_SERIES",
      page: { page: 1, size: 20 }
    });
    expect(mocks.call).toHaveBeenLastCalledWith("ListDatasets", {
      space_id: "space-a",
      data_source_id: "source-a",
      data_kind: "DATA_KIND_TIME_SERIES",
      page: { page: 1, size: 20 }
    });
  });
});

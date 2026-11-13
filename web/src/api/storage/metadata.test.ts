import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ callStorage: vi.fn() }));

vi.mock("./http", () => ({ callStorage: mocks.callStorage }));

import { listDatasets } from "./metadata";

describe("Storage metadata Dataset APIs", () => {
  beforeEach(() => {
    mocks.callStorage.mockReset();
    mocks.callStorage.mockResolvedValue({
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
    expect(mocks.callStorage).toHaveBeenLastCalledWith(
      "ListDatasets",
      {
        space_id: "space-a",
        data_source_id: "source-a",
        data_kind: "DATA_KIND_TIME_SERIES",
        page: { page: 1, size: 20 }
      },
      undefined
    );
  });
});

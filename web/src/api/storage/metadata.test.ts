import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ callStorage: vi.fn() }));

vi.mock("./http", () => ({ callStorage: mocks.callStorage }));

import { listDatasets } from "./metadata";

describe("Storage metadata DataNode APIs", () => {
  beforeEach(() => {
    mocks.callStorage.mockReset();
    mocks.callStorage.mockResolvedValue({
      ret_info: { code: 0, msg: "" },
      node: { node_id: "node-a", name: "节点 A", service_target: "trpc://storage-a:20200", status: "active" },
      items: [],
      datasets: [],
      page_result: { page: 1, size: 20, total: 0, has_more: false, next_cursor: "" },
      dataset_revision: "7",
      checks: [],
      ready: true
    });
  });

  it("preserves supported Proto JSON Dataset filters", async () => {
    await listDatasets({
      space_id: "space-a",
      data_source_id: "source-a",
      data_node_id: "node-a",
      data_kind: "DATA_KIND_TIME_SERIES",
      page: { page: 1, size: 20 }
    });
    expect(mocks.callStorage).toHaveBeenLastCalledWith(
      "ListDatasets",
      {
        space_id: "space-a",
        data_source_id: "source-a",
        data_node_id: "node-a",
        data_kind: "DATA_KIND_TIME_SERIES",
        page: { page: 1, size: 20 }
      },
      undefined
    );
  });
});

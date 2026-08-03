import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ callStorage: vi.fn() }));

vi.mock("./http", () => ({ callStorage: mocks.callStorage }));

import {
  activateDataset,
  checkDatasetActivation,
  deleteDataNode,
  getDataNode,
  listDataNodes,
  listDatasets,
  rebindDatasetDataNode,
  updateDataNode
} from "./metadata";
import fs from "node:fs";
import path from "node:path";

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

  it("uses direct DataNode management RPCs without a browser registration API", async () => {
    const source = fs.readFileSync(path.resolve(__dirname, "metadata.ts"), "utf8");
    expect(source).not.toContain("RegisterDataNode");
    expect(source).not.toContain("registerDataNode");

    await getDataNode({ node_id: "node-a" });
    expect(mocks.callStorage).toHaveBeenLastCalledWith("GetDataNode", { node_id: "node-a" });

    await listDataNodes({ status: "active", page: { page: 2, size: 50 } });
    expect(mocks.callStorage).toHaveBeenLastCalledWith("ListDataNodes", { status: "active", page: { page: 2, size: 50 } });

    await updateDataNode({ node_id: "node-a", name: "节点 A+", status: "disabled" });
    expect(mocks.callStorage).toHaveBeenLastCalledWith("UpdateDataNode", {
      node_id: "node-a",
      name: "节点 A+",
      status: "disabled"
    });

    await deleteDataNode({ node_id: "node-a" });
    expect(mocks.callStorage).toHaveBeenLastCalledWith("DeleteDataNode", { node_id: "node-a" });
  });

  it("preserves supported Proto JSON Dataset filters", async () => {
    await listDatasets({
      space_id: "space-a",
      data_source_id: "source-a",
      data_node_id: "node-a",
      data_kind: "DATA_KIND_TIME_SERIES",
      page: { page: 1, size: 20 }
    });
    expect(mocks.callStorage).toHaveBeenLastCalledWith("ListDatasets", {
      space_id: "space-a",
      data_source_id: "source-a",
      data_node_id: "node-a",
      data_kind: "DATA_KIND_TIME_SERIES",
      page: { page: 1, size: 20 }
    });
  });

  it("uses the exact revision-guarded activation and rebind payloads", async () => {
    await checkDatasetActivation({ space_id: "space-a", dataset_id: "dataset-a" });
    expect(mocks.callStorage).toHaveBeenLastCalledWith("CheckDatasetActivation", {
      space_id: "space-a",
      dataset_id: "dataset-a"
    });

    await activateDataset({ space_id: "space-a", dataset_id: "dataset-a", expected_revision: "7" });
    expect(mocks.callStorage).toHaveBeenLastCalledWith("ActivateDataset", {
      space_id: "space-a",
      dataset_id: "dataset-a",
      expected_revision: "7"
    });

    await rebindDatasetDataNode({
      space_id: "space-a",
      dataset_id: "dataset-a",
      data_node_id: "node-b",
      expected_revision: 8
    });
    expect(mocks.callStorage).toHaveBeenLastCalledWith("RebindDatasetDataNode", {
      space_id: "space-a",
      dataset_id: "dataset-a",
      data_node_id: "node-b",
      expected_revision: 8
    });
  });
});

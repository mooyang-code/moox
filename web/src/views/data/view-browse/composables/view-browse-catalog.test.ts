import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getDataset: vi.fn()
}));

vi.mock("@/api/storage/metadata", async () => {
  const actual = await vi.importActual<typeof import("@/api/storage/metadata")>("@/api/storage/metadata");
  return { ...actual, getDataset: mocks.getDataset };
});

import { loadTargetedDatasets } from "./view-browse-catalog";

describe("loadTargetedDatasets", () => {
  beforeEach(() => {
    mocks.getDataset.mockReset();
  });

  it("fetches only the requested unique datasets", async () => {
    mocks.getDataset.mockImplementation(({ dataset_id }: { dataset_id: string }) =>
      Promise.resolve({ dataset: { space_id: "crypto", dataset_id } })
    );

    const result = await loadTargetedDatasets("crypto", [" dataset_a ", "dataset_b", "dataset_a", ""]);

    expect(mocks.getDataset).toHaveBeenCalledTimes(2);
    expect(mocks.getDataset).toHaveBeenNthCalledWith(1, { space_id: "crypto", dataset_id: "dataset_a" });
    expect(mocks.getDataset).toHaveBeenNthCalledWith(2, { space_id: "crypto", dataset_id: "dataset_b" });
    expect(result.map(item => item.dataset_id)).toEqual(["dataset_a", "dataset_b"]);
  });

  it("avoids metadata calls without a space or dataset IDs", async () => {
    await expect(loadTargetedDatasets("crypto", [])).resolves.toEqual([]);
    await expect(loadTargetedDatasets("", ["dataset_a"])).resolves.toEqual([]);
    expect(mocks.getDataset).not.toHaveBeenCalled();
  });

  it("rejects a partial catalog when any requested dataset is unavailable", async () => {
    mocks.getDataset.mockImplementation(({ dataset_id }: { dataset_id: string }) =>
      dataset_id === "dataset_b"
        ? Promise.reject(new Error("dataset not found"))
        : Promise.resolve({ dataset: { space_id: "crypto", dataset_id } })
    );

    await expect(loadTargetedDatasets("crypto", ["dataset_a", "dataset_b"])).rejects.toThrow("dataset not found");
  });

  it("rejects metadata whose identities do not match the requested space and IDs", async () => {
    mocks.getDataset.mockResolvedValue({ dataset: { space_id: "other", dataset_id: "dataset_a" } });

    await expect(loadTargetedDatasets("crypto", ["dataset_a"])).rejects.toThrow("数据集身份与请求不一致");
  });
});

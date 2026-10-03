import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getDataset: vi.fn(),
  getView: vi.fn()
}));

vi.mock("@/api/storage/metadata", async () => {
  const actual = await vi.importActual<typeof import("@/api/storage/metadata")>("@/api/storage/metadata");
  return { ...actual, getDataset: mocks.getDataset, getView: mocks.getView };
});

import { loadTargetedViewCatalog } from "./view-browse-catalog";

describe("loadTargetedViewCatalog", () => {
  beforeEach(() => {
    mocks.getDataset.mockReset();
    mocks.getView.mockReset();
  });

  it("fetches only the requested views and their unique datasets", async () => {
    mocks.getView.mockImplementation(({ view_id }: { view_id: string }) =>
      Promise.resolve({ view: { space_id: "crypto", view_id, dataset_id: "dataset-a" } })
    );
    mocks.getDataset.mockResolvedValue({ dataset: { space_id: "crypto", dataset_id: "dataset-a" } });

    const result = await loadTargetedViewCatalog("crypto", [" view-a ", "view-b", "view-a", ""]);

    expect(mocks.getView).toHaveBeenCalledTimes(2);
    expect(mocks.getView).toHaveBeenNthCalledWith(1, { space_id: "crypto", view_id: "view-a" });
    expect(mocks.getView).toHaveBeenNthCalledWith(2, { space_id: "crypto", view_id: "view-b" });
    expect(mocks.getDataset).toHaveBeenCalledTimes(1);
    expect(mocks.getDataset).toHaveBeenCalledWith({ space_id: "crypto", dataset_id: "dataset-a" });
    expect(result.views).toHaveLength(2);
    expect(result.datasets).toHaveLength(1);
  });

  it("avoids metadata calls without a space or view IDs", async () => {
    await expect(loadTargetedViewCatalog("crypto", [])).resolves.toEqual({ views: [], datasets: [] });
    await expect(loadTargetedViewCatalog("", ["view-a"])).resolves.toEqual({ views: [], datasets: [] });
    expect(mocks.getView).not.toHaveBeenCalled();
    expect(mocks.getDataset).not.toHaveBeenCalled();
  });

  it("rejects a partial catalog when any requested view or dataset is unavailable", async () => {
    mocks.getView.mockImplementation(({ view_id }: { view_id: string }) =>
      view_id === "missing-view"
        ? Promise.reject(new Error("view not found"))
        : Promise.resolve({ view: { space_id: "crypto", view_id, dataset_id: `dataset-${view_id}` } })
    );

    await expect(loadTargetedViewCatalog("crypto", ["view-a", "missing-view", "view-b"])).rejects.toThrow("view not found");
  });

  it("rejects a partial dataset catalog", async () => {
    mocks.getView.mockImplementation(({ view_id }: { view_id: string }) =>
      Promise.resolve({ view: { space_id: "crypto", view_id, dataset_id: `dataset-${view_id}` } })
    );
    mocks.getDataset.mockImplementation(({ dataset_id }: { dataset_id: string }) =>
      dataset_id === "dataset-view-b"
        ? Promise.reject(new Error("dataset not found"))
        : Promise.resolve({ dataset: { space_id: "crypto", dataset_id } })
    );

    await expect(loadTargetedViewCatalog("crypto", ["view-a", "view-b"])).rejects.toThrow("dataset not found");
  });

  it("rejects metadata whose identities do not match the requested space and IDs", async () => {
    mocks.getView.mockResolvedValue({ view: { space_id: "other", view_id: "view-a", dataset_id: "dataset-a" } });

    await expect(loadTargetedViewCatalog("crypto", ["view-a"])).rejects.toThrow("视图身份与请求不一致");
  });

  it("surfaces an error when no requested view has an available dataset", async () => {
    mocks.getView.mockResolvedValue({ view: { space_id: "crypto", view_id: "view-a", dataset_id: "dataset-a" } });
    mocks.getDataset.mockRejectedValue(new Error("dataset unavailable"));

    await expect(loadTargetedViewCatalog("crypto", ["view-a"])).rejects.toThrow("dataset unavailable");
  });

  it("surfaces an error when no requested view can be loaded", async () => {
    mocks.getView.mockRejectedValue(new Error("view unavailable"));

    await expect(loadTargetedViewCatalog("crypto", ["view-a", "view-b"])).rejects.toThrow("view unavailable");
  });
});

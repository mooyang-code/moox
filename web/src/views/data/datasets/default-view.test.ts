import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/api/storage/metadata", () => ({ createView: vi.fn(), listViews: vi.fn() }));

import { createView, listViews } from "@/api/storage/metadata";
import { buildDefaultView, defaultViewIdForDataset, ensureDefaultView } from "./default-view";

const unsupportedDatasetId = "m" + "dataset_binance_kline_1m";

describe("default dataset index", () => {
  beforeEach(() => vi.resetAllMocks());
  it("builds a valid view id from dataset ids", () => {
    expect(defaultViewIdForDataset("dataset_binance_kline_1m")).toBe("view_binance_kline_1m");
    expect(() => defaultViewIdForDataset(unsupportedDatasetId)).toThrow("dataset_");
  });

  it("keeps a single dataset_id on the default view", () => {
    const view = buildDefaultView(
      { space_id: "crypto", dataset_id: "dataset_binance_kline_1m", name: "现货K线", keep_duration: "30d" },
      { ownerModule: "collector", viewRole: "collection_browse", managedBy: "manual" }
    );
    expect(view.dataset_id).toBe("dataset_binance_kline_1m");
    expect(view.view_id).toBe("view_binance_kline_1m");
    expect(view.attributes?.owner_module).toBe("collector");
  });

  it("does not create Factor result Views from the frontend", async () => {
    vi.mocked(listViews).mockResolvedValue({ ret_info: { code: 0, msg: "" }, views: [], page_result: { total: 0 } } as never);
    await expect(
      ensureDefaultView(
        { space_id: "crypto", dataset_id: "dataset_prices_factor", name: "因子结果", keep_duration: "0" },
        { ownerModule: "factor", viewRole: "factor_result", managedBy: "storage" }
      )
    ).rejects.toThrow("Storage");
    expect(createView).not.toHaveBeenCalled();
  });
});

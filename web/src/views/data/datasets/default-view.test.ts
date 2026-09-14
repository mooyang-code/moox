import { describe, expect, it } from "vitest";
import { buildDefaultView, defaultViewIdForDataset } from "./default-view";

describe("default dataset index", () => {
  it("builds a valid view id from collector and merged dataset ids", () => {
    expect(defaultViewIdForDataset("dataset_binance_spot_kline_1m")).toBe("view_binance_spot_kline_1m");
    expect(defaultViewIdForDataset("mdataset_binance_kline_1m")).toBe("view_binance_kline_1m");
  });

  it("keeps a single dataset_id on the default view", () => {
    const view = buildDefaultView(
      { space_id: "crypto", dataset_id: "dataset_binance_spot_kline_1m", name: "现货K线", keep_duration: "30d" },
      { ownerModule: "collector", viewRole: "collection_browse", managedBy: "manual" }
    );
    expect(view.dataset_id).toBe("dataset_binance_spot_kline_1m");
    expect(view.view_id).toBe("view_binance_spot_kline_1m");
    expect(view.attributes?.owner_module).toBe("collector");
  });
});

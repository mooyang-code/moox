import { describe, expect, it } from "vitest";
import type { Dataset } from "@/api/storage/types";
import { datasetMatchesAttribution, datasetRoles, isFactorResultDataset } from "./module-attribution";

function dataset(id: string, attributes?: Record<string, string>): Dataset {
  return {
    space_id: "crypto",
    dataset_id: id,
    name: id,
    data_kind: "DATA_KIND_TIME_SERIES",
    status: "active",
    attributes
  };
}

describe("module-attribution", () => {
  it("角色清单仅包含后端实际写入的三种角色", () => {
    expect(datasetRoles).toEqual(["raw_collection", "kline_resample_result", "factor_result"]);
  });

  it("仅按后端属性判断因子结果数据集，不按 ID 后缀猜测", () => {
    expect(isFactorResultDataset(dataset("ds_1", { owner_module: "factor" }))).toBe(true);
    expect(isFactorResultDataset(dataset("ds_2", { dataset_role: "factor_result" }))).toBe(true);
    expect(isFactorResultDataset(dataset("ds_alpha_factor", { owner_module: "collector", dataset_role: "raw_collection" }))).toBe(
      false
    );
    expect(isFactorResultDataset(dataset("ds_f1a2b", {}))).toBe(false);
    expect(isFactorResultDataset(dataset("ds_plain_factor"))).toBe(false);
  });

  it("按归属模块与角色筛选数据集，并支持包含未标记归属的历史数据集", () => {
    const raw = dataset("ds_raw", { owner_module: "collector", dataset_role: "raw_collection" });
    const resample = dataset("ds_resample", { owner_module: "collector", dataset_role: "kline_resample_result" });
    const factor = dataset("ds_fac", { owner_module: "factor", dataset_role: "factor_result" });
    const unowned = dataset("ds_legacy");
    const filter = {
      ownerModules: ["collector" as const],
      datasetRoles: ["raw_collection" as const, "kline_resample_result" as const],
      includeUnowned: true
    };
    expect(datasetMatchesAttribution(raw, filter)).toBe(true);
    expect(datasetMatchesAttribution(resample, filter)).toBe(true);
    expect(datasetMatchesAttribution(unowned, filter)).toBe(true);
    expect(datasetMatchesAttribution(factor, filter)).toBe(false);
  });
});

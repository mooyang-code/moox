import { describe, expect, it } from "vitest";
import {
  buildCollectionTaskParams,
  buildCollectionTaskPayload,
  parseKlineResampleParams,
  collectionTaskNameError,
  availableExtraFieldIds,
  extraFieldOptionsPresent,
  outputFieldSelectable,
  outputFieldVisible,
  pruneUnavailableFields,
  shouldDropUnavailableFields,
  collectionTaskResultLabel,
  normalizeCollectionTask,
  parseCollectionTaskInput,
  taskFrequency
} from "./collection-task-params";

describe("collection task params", () => {
  it("builds kline input from selected subject tags", () => {
    const params = buildCollectionTaskParams({
      dataType: "kline",
      provider: " Binance ",
      market: "spot",
      frequency: "1h",
      subjectTags: [" binance_spot "]
    });

    expect(params).toEqual({
      subject_tags: ["binance_spot"],
      frequency: "1h"
    });
    expect(params).not.toHaveProperty("target_dataset_id");
  });

  it("requires at least one subject tag for kline input", () => {
    expect(() =>
      buildCollectionTaskParams({
        dataType: "kline",
        provider: "binance",
        market: "spot",
        frequency: "1h",
        subjectTags: []
      })
    ).toThrow("请选择标的标签");
  });

  it("builds resample inputs from the source task only", () => {
    const params = buildCollectionTaskParams({
      dataType: "kline_resample",
      provider: "",
      market: "spot",
      frequency: "45m",
      sourceTaskId: "task-5m"
    });

    // 源数据集、源周期、序列标签由后端推导，前端只提交源任务与目标频率；没有收盘等待与对齐方式。
    expect(params).toEqual({ source_task_id: "task-5m", target_frequency: "45m" });
    expect(params).not.toHaveProperty("settle_delay_ms");
    expect(params).not.toHaveProperty("alignment");
    expect(params).not.toHaveProperty("source_dataset_id");

    expect(
      buildCollectionTaskParams({ dataType: "kline_resample", provider: "", market: "spot", frequency: "60m", sourceTaskId: "t" })
    ).toMatchObject({
      target_frequency: "1h"
    });
    expect(() =>
      buildCollectionTaskParams({ dataType: "kline_resample", provider: "", market: "spot", frequency: "45m" })
    ).toThrow("请选择源采集任务");
    expect(() =>
      buildCollectionTaskParams({ dataType: "kline_resample", provider: "", market: "spot", frequency: " ", sourceTaskId: "t" })
    ).toThrow("请输入采集频率");

    const payload = buildCollectionTaskPayload(
      { dataType: "kline_resample", provider: "", market: "spot", frequency: "5m", sourceTaskId: "task-1m" },
      { task_name: "合成", description: "", space_id: "crypto", creator: "admin", enabled: true }
    );
    expect(payload).not.toHaveProperty("provider");
    expect(payload.collect_params).toEqual({ source_task_id: "task-1m", target_frequency: "5m" });
  });

  it("parses resample routing from collect_params", () => {
    const task = normalizeCollectionTask({
      task_id: "resample-routing",
      data_type: "kline_resample",
      collect_params: {
        provider: "moox",
        market_type: "spot",
        source_dataset_id: "source-bars",
        source_frequency: "1m",
        source_series_tag: "venue:binance",
        target_frequency: "5m",
        source_task_id: "task-1m"
      }
    });

    expect(parseKlineResampleParams(task.collect_params)).toMatchObject({
      provider: "moox",
      market_type: "spot",
      source_dataset_id: "source-bars",
      source_series_tag: "venue:binance",
      target_frequency: "5m",
      source_task_id: "task-1m"
    });
    expect(parseCollectionTaskInput(task)).toMatchObject({ provider: "moox", market: "spot", sourceTaskId: "task-1m" });
  });

  it("统一数据集的合成源：读出 source_series_tags，source_series_tag 为空", () => {
    const params = {
      source_task_id: "task-uni",
      source_dataset_id: "uni-bars",
      source_frequency: "1m",
      source_series_tags: ["venue:binance|market:spot", "venue:binance|market:swap"],
      target_frequency: "5m"
    };
    expect(parseKlineResampleParams(params)).toMatchObject({
      source_series_tag: "",
      source_series_tags: ["venue:binance|market:spot", "venue:binance|market:swap"]
    });
    expect(
      parseCollectionTaskInput({ data_type: "kline_resample", tag_ids: [], collect_params: params }).sourceSeriesTags
    ).toEqual(["venue:binance|market:spot", "venue:binance|market:swap"]);
    expect(parseKlineResampleParams({ ...params, source_series_tags: undefined })).not.toHaveProperty("source_series_tags");
  });

  it("emits result_config separately from the task payload", () => {
    const task = buildCollectionTaskPayload(
      {
        dataType: "kline",
        provider: "binance",
        market: "swap",
        frequency: "6h",
        subjectTags: ["binance_swap"]
      },
      {
        task_name: "  标的同步  ",
        description: "  同步任务 ",
        space_id: "crypto",
        creator: "admin",
        enabled: false
      }
    );

    expect(task).toMatchObject({
      task_name: "标的同步",
      description: "同步任务",
      data_type: "kline",
      tag_ids: ["binance_swap"],
      enabled: false
    });
    expect(task).not.toHaveProperty("provider");
    expect(task).not.toHaveProperty("market_type");
    expect(task.collect_params).not.toHaveProperty("provider");
    expect(task.collect_params).not.toHaveProperty("market_type");
    expect(task.collect_params).not.toHaveProperty("target_dataset_id");
  });

  it("validates trimmed Unicode task names", () => {
    expect(collectionTaskNameError("   ")).toBe("请输入任务名称");
    expect(collectionTaskNameError("a".repeat(81))).toBe("任务名称不能超过 80 个字符");
    expect(collectionTaskNameError("  正常任务  ")).toBeUndefined();
  });

  it("normalizes server task data and derives its frequency", () => {
    const task = normalizeCollectionTask({
      task_id: "task-1",
      task_name: "任务一",
      data_type: "kline_resample",
      tag_ids: ["source_tag"],
      enabled: "false",
      collect_params: '{"provider":"moox","market_type":"spot","target_frequency":"5m"}',
      result: { view_id: "view-1", status: "ready" }
    });

    expect(task.enabled).toBe(false);
    expect(task.result?.view_id).toBe("view-1");
    expect(taskFrequency(task)).toBe("5m");
    expect(parseCollectionTaskInput(task)).toMatchObject({
      dataType: "kline_resample",
      frequency: "5m",
      provider: "moox"
    });
  });
});

describe("collection task result label", () => {
  const result = (status: string) => ({ view_id: "view_task_kline_1m", status });

  it("treats an uninspected list entry of a prepared task as available", () => {
    expect(collectionTaskResultLabel({ prepare_state: "ready", result: result("unknown") })).toBe("结果可用");
  });

  it("keeps tasks that are still preparing in the preparing state", () => {
    expect(collectionTaskResultLabel({ prepare_state: "pending", result: result("unknown") })).toBe("结果准备中");
    expect(collectionTaskResultLabel({ prepare_state: "waiting_view", result: result("pending") })).toBe("结果准备中");
    expect(collectionTaskResultLabel({ prepare_state: "ready", result: { status: "unknown" } })).toBe("结果准备中");
  });

  it("follows the inspected Storage status from task details", () => {
    expect(collectionTaskResultLabel({ prepare_state: "ready", result: result("ready") })).toBe("结果可用");
    expect(collectionTaskResultLabel({ prepare_state: "ready", result: result("pending") })).toBe("结果准备中");
    expect(collectionTaskResultLabel({ prepare_state: "ready", result: result("error") })).toBe("结果异常");
    expect(collectionTaskResultLabel({ prepare_state: "error", result: result("unknown") })).toBe("结果异常");
    expect(collectionTaskResultLabel({ prepare_state: "ready", last_error: "boom", result: result("unknown") })).toBe("结果异常");
  });
});

describe("附加输出字段", () => {
  // 与后端 GetDataTypeConfigWithFields 的 extra_fields 同形：资金费率只有加密永续、1h/1d；A 股附加字段只有股票日线。
  const options = [
    { field_id: "funding_rate", market_id: "crypto", instrument_type: "swap", frequencies: ["1h", "1d"], provider_id: "binance" },
    { field_id: "adj_factor", market_id: "stockcn", instrument_type: "equity", frequencies: ["1d"], provider_id: "sina_hfq" },
    { field_id: "sw_l1", market_id: "stockcn", instrument_type: "equity", frequencies: ["1d"], provider_id: "swsresearch" }
  ];
  const swap = { market: "swap" };
  const spot = { market: "spot" };
  const equity = { market: "equity" };
  const available = (spaceId: string, frequency: string, routes: Array<{ market: string }>, dataType = "kline") =>
    [...availableExtraFieldIds({ dataType, spaceId, frequency, routes, options })].sort();

  it("按市场、品种与频率给出可选的附加字段", () => {
    expect(available("stockcn", "1d", [equity])).toEqual(["adj_factor", "sw_l1"]);
    expect(available("stockcn", "5m", [equity])).toEqual([]);
    // 别的市场的字段不出现
    expect(available("crypto", "1d", [swap])).toEqual(["funding_rate"]);
    expect(available("crypto", "1d", [equity])).toEqual([]);
    // 没选标签、不是 K 线采集、接口没给清单时都没有
    expect(available("stockcn", "1d", [])).toEqual([]);
    expect(available("stockcn", "1d", [equity], "kline_resample")).toEqual([]);
    expect([
      ...availableExtraFieldIds({ dataType: "kline", spaceId: "stockcn", frequency: "1d", routes: [equity], options: [] })
    ]).toEqual([]);
    // 空间 ID、频率写法不同也能对上
    expect(available(" StockCN ", "1D", [equity])).toEqual(["adj_factor", "sw_l1"]);
  });

  it("资金费率的显示与原来一致：只在永续、1h 或 1d、非统一数据集时出现", () => {
    expect(available("crypto", "1h", [swap])).toEqual(["funding_rate"]);
    expect(available("crypto", "1d", [swap, swap])).toEqual(["funding_rate"]);
    for (const frequency of ["1m", "5m", "30m", "4h", "1w", "1mo", ""]) {
      expect(available("crypto", frequency, [swap])).toEqual([]);
    }
    expect(available("crypto", "1h", [spot])).toEqual([]);
    // 现货加永续的统一数据集选不了
    expect(available("crypto", "1h", [spot, swap])).toEqual([]);
    expect(available("crypto", "1h", [])).toEqual([]);
    expect(available("crypto", "1h", [swap], "kline_resample")).toEqual([]);
  });

  it("字段清单只显示基础 K 线字段和当前可选的附加字段", () => {
    const now = new Set(["adj_factor"]);
    expect(outputFieldVisible("close", now)).toBe(true);
    expect(outputFieldVisible("adj_factor", now)).toBe(true);
    expect(outputFieldVisible("sw_l1", now)).toBe(false);
    expect(outputFieldVisible("funding_rate", new Set())).toBe(false);
  });

  it("条件不满足时去掉已选的附加字段", () => {
    const selected = ["close", "adj_factor", "sw_l1"];
    // 频率从 1d 改成 5m：两个附加字段都去掉
    expect(pruneUnavailableFields(selected, new Set(available("stockcn", "5m", [equity])), true)).toEqual(["close"]);
    // 只剩一个可选
    expect(pruneUnavailableFields(selected, new Set(["adj_factor"]), true)).toEqual(["close", "adj_factor"]);
    // 没有要去掉的、或不该去掉时原样返回同一个数组
    expect(pruneUnavailableFields(selected, new Set(["adj_factor", "sw_l1"]), true)).toBe(selected);
    expect(pruneUnavailableFields(selected, new Set(), false)).toBe(selected);
    const plain = ["close"];
    expect(pruneUnavailableFields(plain, new Set(), true)).toBe(plain);
  });

  it("旧版 Collector 的响应没有 extra_fields 时不算读到附加字段清单", () => {
    expect(extraFieldOptionsPresent({ extra_fields: [] })).toBe(true);
    expect(extraFieldOptionsPresent({ extra_fields: [{ field_id: "funding_rate" }] })).toBe(true);
    expect(extraFieldOptionsPresent({})).toBe(false);
    expect(extraFieldOptionsPresent({ extra_fields: null })).toBe(false);
    expect(extraFieldOptionsPresent(undefined)).toBe(false);
    // 编辑带 funding_rate 的任务：旧版响应下保留这一列，新版响应明确给出空清单时才去掉
    const selected = ["close", "funding_rate"];
    const drop = (detail: { extra_fields?: unknown }) => shouldDropUnavailableFields(true, 1, extraFieldOptionsPresent(detail));
    expect(pruneUnavailableFields(selected, new Set(), drop({}))).toBe(selected);
    expect(pruneUnavailableFields(selected, new Set(), drop({ extra_fields: [] }))).toEqual(["close"]);
  });

  it("编辑任务时标签或附加字段清单还没读到就不去掉已选项", () => {
    expect(shouldDropUnavailableFields(true, 0, true)).toBe(false);
    expect(shouldDropUnavailableFields(true, 1, false)).toBe(false);
    expect(shouldDropUnavailableFields(true, 1, true)).toBe(true);
    // 新建时没有要保护的已选项，照常清理
    expect(shouldDropUnavailableFields(false, 0, false)).toBe(true);
    expect(shouldDropUnavailableFields(false, 1, true)).toBe(true);
  });

  it("编辑任务时不能新增字段，只能取消或重新勾上原有的", () => {
    // 新建：都能勾
    expect(outputFieldSelectable("adj_factor", false, [])).toBe(true);
    // 编辑：原来有的可以重新勾上，原来没有的（基础字段也一样）不能勾
    expect(outputFieldSelectable("adj_factor", true, ["close", "adj_factor"])).toBe(true);
    expect(outputFieldSelectable("sw_l1", true, ["close", "adj_factor"])).toBe(false);
    expect(outputFieldSelectable("volume", true, ["close", "adj_factor"])).toBe(false);
    // 原来没有显式选择（默认字段）：可以收窄到基础字段，不能勾附加字段
    expect(outputFieldSelectable("close", true, [])).toBe(true);
    expect(outputFieldSelectable("adj_factor", true, [])).toBe(false);
  });
});

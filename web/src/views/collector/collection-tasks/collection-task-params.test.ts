import { describe, expect, it } from "vitest";
import {
  buildCollectionTaskParams,
  buildCollectionTaskPayload,
  parseKlineResampleParams,
  collectionSourceMatches,
  collectionTaskNameError,
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

  it("builds resample inputs and preserves an explicit zero delay", () => {
    const params = buildCollectionTaskParams({
      dataType: "kline_resample",
      provider: "moox",
      market: "spot",
      frequency: "5m",
      sourceId: "source-bars",
      sourceFrequency: "1m",
      sourceSeriesTag: "venue:binance",
      settleDelayMS: 0
    });

    expect(params).toMatchObject({
      source_dataset_id: "source-bars",
      target_frequency: "5m",
      alignment: "epoch_utc",
      settle_delay_ms: 0
    });
    expect(params).not.toHaveProperty("target_dataset_id");

    const payload = buildCollectionTaskPayload(
      {
        dataType: "kline_resample",
        provider: "moox",
        market: "spot",
        frequency: "5m",
        sourceId: "source-bars",
        sourceFrequency: "1m",
        sourceSeriesTag: "venue:binance"
      },
      { task_name: "重采样", description: "", space_id: "crypto", creator: "admin", enabled: true }
    );
    expect(payload).not.toHaveProperty("provider");
    expect(payload).not.toHaveProperty("market_type");
    expect(payload.collect_params).toMatchObject({ provider: "moox", market_type: "spot" });
  });

  it("parses resample routing only from collect_params", () => {
    const task = normalizeCollectionTask({
      task_id: "resample-routing",
      data_type: "kline_resample",
      provider: "legacy-top-level",
      market_type: "swap",
      collect_params: {
        provider: "moox",
        market_type: "spot",
        source_dataset_id: "source-bars",
        source_frequency: "1m",
        source_series_tag: "venue:binance",
        target_frequency: "5m",
        alignment: "epoch_utc"
      }
    });

    expect(parseKlineResampleParams(task.collect_params)).toMatchObject({
      provider: "moox",
      market_type: "spot",
      source_dataset_id: "source-bars",
      source_series_tag: "venue:binance",
      target_frequency: "5m"
    });
    expect(parseCollectionTaskInput(task)).toMatchObject({ provider: "moox", market: "spot" });
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
      provider: "legacy-provider-must-be-ignored",
      market_type: "swap",
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

  it("matches resample source options by market and supported frequency", () => {
    const source = {
      source_id: "source-bars",
      data_source_id: "crypto",
      data_kind: "DATA_KIND_TIME_SERIES",
      attributes: { market_type: "spot" },
      freqs: ["1H"]
    };

    expect(collectionSourceMatches(source, "binance", "kline", "spot", "1h")).toBe(true);
    expect(collectionSourceMatches(source, "binance", "kline", "swap", "1h")).toBe(false);
    expect(collectionSourceMatches(source, "binance", "kline", "spot", "4h")).toBe(false);
    expect(collectionSourceMatches(source, "binance", "kline_resample", "spot", "1h")).toBe(true);
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

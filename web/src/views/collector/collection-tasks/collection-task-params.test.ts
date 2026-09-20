import { describe, expect, it } from "vitest";
import {
  buildCollectionTaskParams,
  buildCollectionTaskPayload,
  collectionSourceMatches,
  collectionTaskNameError,
  normalizeCollectionTask,
  parseCollectionTaskInput,
  taskFrequency
} from "./collection-task-params";

describe("collection task params", () => {
  it("builds task-owned result input without a caller-selected result identity", () => {
    const params = buildCollectionTaskParams({
      dataType: "kline",
      provider: " Binance ",
      market: "spot",
      frequency: "1h",
      symbolSource: "dataset",
      symbolSourceId: "symbols"
    });

    expect(params).toEqual({
      provider: "binance",
      market_type: "spot",
      symbol_source: "dataset",
      symbol_dataset_id: "symbols",
      frequency: "1h"
    });
    expect(params).not.toHaveProperty("target_dataset_id");
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
  });

  it("emits result_config separately from the task payload", () => {
    const task = buildCollectionTaskPayload(
      {
        dataType: "instrument",
        provider: "binance",
        market: "swap",
        frequency: "6h"
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
      data_type: "instrument",
      provider: "binance",
      market_type: "swap",
      enabled: false
    });
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
      provider: "moox",
      market_type: "spot",
      enabled: "false",
      collect_params: '{"target_frequency":"5m"}',
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

  it("matches source options by market and supported frequency", () => {
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
    expect(
      collectionSourceMatches(
        {
          source_id: "symbols",
          data_source_id: "binance",
          data_kind: "DATA_KIND_RECORD",
          attributes: { market_type: "spot" }
        },
        "binance",
        "instrument",
        "spot"
      )
    ).toBe(true);
  });
});

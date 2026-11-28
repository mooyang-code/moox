import { describe, expect, it } from "vitest";
import {
  buildRunningQuery,
  emptyKind,
  isPendingSeedInstance,
  isTrialInstance,
  listKey,
  parseRunningQuery
} from "./running-model";

describe("running page model", () => {
  it("normalizes the URL state", () => {
    expect(parseRunningQuery({ page: "0", enabled: "maybe", strategy_id: " s1 " })).toEqual({
      strategyId: "s1",
      enabled: "",
      page: 1,
      create: false
    });
    expect(parseRunningQuery({ enabled: "false", create: "1", page: "3" })).toMatchObject({
      enabled: "false",
      create: true,
      page: 3
    });
  });

  it("omits defaults when writing the URL", () => {
    expect(buildRunningQuery({ strategyId: "", enabled: "", page: 1, create: false })).toEqual({});
    expect(buildRunningQuery({ strategyId: "s1", enabled: "true", page: 2, create: true })).toEqual({
      create: "1",
      enabled: "true",
      page: "2",
      strategy_id: "s1"
    });
  });

  it("changes the list key only for list conditions", () => {
    const base = { strategyId: "s1", enabled: "" as const, page: 2, create: false };
    expect(listKey({ ...base, create: true })).toBe(listKey(base));
    expect(listKey({ ...base, page: 3 })).not.toBe(listKey(base));
  });

  it("distinguishes empty states", () => {
    expect(emptyKind({ loading: true, error: "", count: 0, filtered: false })).toBe("none");
    expect(emptyKind({ loading: false, error: "x", count: 0, filtered: true })).toBe("error");
    expect(emptyKind({ loading: false, error: "", count: 0, filtered: true })).toBe("no-match");
    expect(emptyKind({ loading: false, error: "", count: 0, filtered: false })).toBe("no-instances");
    expect(emptyKind({ loading: false, error: "", count: 2, filtered: false })).toBe("none");
  });

  it("marks enabled, account-bound, unconfirmed instances as trial", () => {
    expect(isTrialInstance({ enabled: true, logical_account_id: "la-1", confirmed_at: "" })).toBe(true);
    expect(isTrialInstance({ enabled: true, logical_account_id: "la-1" })).toBe(true);
    expect(isTrialInstance({ enabled: true, logical_account_id: "la-1", confirmed_at: "1794704400000" })).toBe(false);
    // 没挂账户的实例启用即确认；停用的实例不在试运行。
    expect(isTrialInstance({ enabled: true, logical_account_id: "", confirmed_at: "" })).toBe(false);
    expect(isTrialInstance({ enabled: false, logical_account_id: "la-1", confirmed_at: "" })).toBe(false);
    expect(isTrialInstance(null)).toBe(false);
  });
});

describe("isPendingSeedInstance", () => {
  const resolved = JSON.stringify({
    bar: "5m",
    tasks: { swap: { dataset_id: "ds_factor", source_dataset_id: "ds_kline" } }
  });
  const instance = { enabled: true, resolved_json: resolved };
  const running = (dataset_id: string, frequency = "5m") => ({ dataset_id, frequency, status: "running" });

  it("上游数据集（含来源数据集）正在补齐、周期一致时待生效", () => {
    expect(isPendingSeedInstance(instance, [running("ds_kline")])).toBe(true);
    expect(isPendingSeedInstance(instance, [running("ds_factor", "5M")])).toBe(true);
  });

  it("未启用、周期不同、已完成或数据集无关时不算", () => {
    expect(isPendingSeedInstance({ ...instance, enabled: false }, [running("ds_kline")])).toBe(false);
    expect(isPendingSeedInstance(instance, [running("ds_kline", "1m")])).toBe(false);
    expect(isPendingSeedInstance(instance, [{ ...running("ds_kline"), status: "done" }])).toBe(false);
    expect(isPendingSeedInstance(instance, [running("other")])).toBe(false);
  });

  it("没有补齐数据或 resolved_json 不可用时按没有处理", () => {
    expect(isPendingSeedInstance(instance, undefined)).toBe(false);
    expect(isPendingSeedInstance({ enabled: true, resolved_json: "{}" }, [running("ds_kline")])).toBe(false);
    expect(isPendingSeedInstance({ enabled: true, resolved_json: "不是 JSON" }, [running("ds_kline")])).toBe(false);
    expect(isPendingSeedInstance(null, [running("ds_kline")])).toBe(false);
  });
});

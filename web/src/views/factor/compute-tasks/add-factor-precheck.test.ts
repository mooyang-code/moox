import { describe, expect, it } from "vitest";
import type { FactorDef } from "@/api/factor/types";
import { precheckAddFactor, type PrecheckMember } from "./add-factor-precheck";

const def = (patch: Partial<FactorDef> = {}): FactorDef => ({
  factor_id: "bias",
  name: "bias",
  factor_type: "timeseries",
  spec_hash: "h",
  input_columns: ["close"],
  outputs: ["bias"],
  lookback_periods: 20,
  allow_partial_universe: false,
  created_at: "",
  updated_at: "",
  ...patch
});

const ema = (): FactorDef =>
  def({
    factor_id: "ema",
    outputs: ["ema"],
    params: [
      { name: "span", type: "int", min: 2, max: 4825, default_value: 20 },
      { name: "warmup", type: "int", min: 0, max: 50000, default_value: 0 }
    ]
  });

const member = (patch: Partial<PrecheckMember> = {}): PrecheckMember => ({
  factor_id: "bias",
  member_key: "bias",
  kind: "fixed",
  output_columns: ["bias"],
  ...patch
});

const base = { sourceColumns: ["open", "high", "low", "close", "volume"], members: [] as PrecheckMember[] };

describe("precheckAddFactor", () => {
  it("a factor without params can only be a fixed member once", () => {
    expect(precheckAddFactor({ ...base, candidate: def(), members: [member()] })).toEqual({
      ok: false,
      reason: "已在该计算任务中"
    });
  });

  it("missing input column reports the column name", () => {
    const result = precheckAddFactor({ ...base, candidate: def({ input_columns: ["close", "funding_rate"] }) });
    expect(result).toEqual({ ok: false, reason: "输入列 funding_rate 不在源数据集中" });
  });

  it("output colliding with another member's result column is rejected", () => {
    const other = member({ factor_id: "other", member_key: "other", output_columns: ["bias"] });
    const result = precheckAddFactor({
      ...base,
      candidate: def({ factor_id: "bias2", outputs: ["bias2"] }),
      members: [other],
      sourceColumns: ["close", "bias2"]
    });
    expect(result).toEqual({ ok: false, reason: "输出列 bias2 与源数据集列重名", fixable: true });
    const clash = precheckAddFactor({
      ...base,
      candidate: def({ factor_id: "bias2", outputs: ["bias2"] }),
      members: [member({ member_key: "other", output_columns: ["bias2"] })]
    });
    expect(clash).toEqual({ ok: false, reason: "输出列 bias2 与成员 other 的输出列重名", fixable: true });
  });

  it("valid candidate is selectable and reports its member key and columns", () => {
    expect(precheckAddFactor({ ...base, candidate: def() })).toEqual({ ok: true, memberKey: "bias", columns: ["bias"] });
  });

  it("skips column checks while the source columns are still unknown", () => {
    expect(precheckAddFactor({ candidate: def({ input_columns: ["x"] }), members: [], sourceColumns: null })).toEqual({
      ok: true,
      memberKey: "bias",
      columns: ["bias"]
    });
  });

  it("derives the member key from the params; all defaults keep the factor id", () => {
    expect(precheckAddFactor({ ...base, candidate: ema() })).toMatchObject({ ok: true, memberKey: "ema" });
    expect(precheckAddFactor({ ...base, candidate: ema(), params: { warmup: 50000 } })).toMatchObject({
      ok: true,
      memberKey: "ema__20_50000"
    });
    expect(precheckAddFactor({ ...base, candidate: ema(), params: { span: 50, warmup: 20000 } })).toMatchObject({
      ok: true,
      memberKey: "ema__50_20000",
      columns: ["ema__50_20000"]
    });
  });

  it("a variable member is keyed by the factor id plus __var, one per factor", () => {
    const variable = precheckAddFactor({ ...base, candidate: ema(), kind: "var" });
    expect(variable).toEqual({ ok: true, memberKey: "ema__var", columns: ["ema__var"] });
    const existing = member({ factor_id: "ema", member_key: "ema__var", kind: "var", output_columns: ["ema__var"] });
    expect(precheckAddFactor({ ...base, candidate: ema(), kind: "var", members: [existing] })).toEqual({
      ok: false,
      reason: "已有这个因子的可变成员"
    });
    // 固定成员和可变成员互不冲突。
    expect(precheckAddFactor({ ...base, candidate: ema(), members: [existing] })).toMatchObject({ ok: true, memberKey: "ema" });
  });

  it("the same factor with different params can be added again; the same params cannot", () => {
    const defaults = member({ factor_id: "ema", member_key: "ema", params: { span: 20, warmup: 0 }, output_columns: ["ema"] });
    expect(precheckAddFactor({ ...base, candidate: ema(), members: [defaults], params: { warmup: 50000 } })).toMatchObject({
      ok: true,
      memberKey: "ema__20_50000"
    });
    expect(precheckAddFactor({ ...base, candidate: ema(), members: [defaults], params: { span: 20, warmup: 0 } })).toEqual({
      ok: false,
      reason: "已有相同参数的成员 ema",
      fixable: true
    });
  });

  it("rejects out-of-range or fractional params", () => {
    expect(precheckAddFactor({ ...base, candidate: ema(), params: { span: 1 } })).toEqual({
      ok: false,
      reason: "参数 span 需在 2 ~ 4825 之间",
      fixable: true
    });
    expect(precheckAddFactor({ ...base, candidate: ema(), params: { span: 20.5 } })).toEqual({
      ok: false,
      reason: "参数 span 必须是整数",
      fixable: true
    });
  });

  it("reports an over-long generated member key so the params can be changed", () => {
    const long = def({
      factor_id: "a_long_factor_identifier",
      outputs: ["a_long_factor_identifier"],
      params: ["x", "y", "z"].map(name => ({ name, type: "float" as const, min: 0, max: 1e6, default_value: 1 }))
    });
    const result = precheckAddFactor({ ...base, candidate: long, params: { x: 123456.789, y: 123456.789, z: 123456.789 } });
    expect(result.ok).toBe(false);
    expect(result.ok === false && result.reason).toContain("超过");
    expect(result.ok === false && result.fixable).toBe(true);
  });

  it("multi-output factors get one column per output, prefixed by the member key", () => {
    const boll = def({ factor_id: "boll", outputs: ["upper", "mid", "lower"] });
    expect(precheckAddFactor({ ...base, candidate: boll })).toEqual({
      ok: true,
      memberKey: "boll",
      columns: ["boll__upper", "boll__mid", "boll__lower"]
    });
  });
});

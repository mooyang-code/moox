import { describe, expect, it } from "vitest";
import type { FactorDef } from "@/api/factor/types";
import { precheckAddFactor, type PrecheckMember } from "./add-factor-precheck";

const def = (patch: Partial<FactorDef> = {}): FactorDef => ({
  factor_id: "Bias",
  name: "Bias",
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
    factor_id: "Ema",
    outputs: ["ema"],
    params: [
      { name: "span", type: "int", min: 2, max: 4825, default_value: 20 },
      { name: "warmup", type: "int", min: 0, max: 50000, default_value: 0 }
    ]
  });

const member = (patch: Partial<PrecheckMember> = {}): PrecheckMember => ({
  factor_id: "Bias",
  alias: "Bias",
  output_columns: ["bias"],
  ...patch
});

const base = { sourceColumns: ["open", "high", "low", "close", "volume"], members: [] as PrecheckMember[] };

describe("precheckAddFactor", () => {
  it("a factor without params can only be a member once", () => {
    expect(precheckAddFactor({ ...base, candidate: def(), members: [member()] })).toEqual({ ok: false, reason: "已在该计算任务中" });
  });

  it("missing input column reports the column name", () => {
    const result = precheckAddFactor({ ...base, candidate: def({ input_columns: ["close", "funding_rate"] }) });
    expect(result).toEqual({ ok: false, reason: "输入列 funding_rate 不在源数据集中" });
  });

  it("output colliding with another member's result column is rejected", () => {
    const other = member({ factor_id: "Other", alias: "Other", output_columns: ["bias"] });
    const result = precheckAddFactor({ ...base, candidate: def({ factor_id: "Bias2" }), members: [other] });
    expect(result).toEqual({ ok: false, reason: "输出列 bias 与成员 Other 的输出列重名", fixable: true });
  });

  it("output colliding with source column is rejected", () => {
    const result = precheckAddFactor({ ...base, candidate: def({ outputs: ["close"] }) });
    expect(result).toEqual({ ok: false, reason: "输出列 close 与源数据集列重名", fixable: true });
  });

  it("valid candidate is selectable and reports its alias", () => {
    expect(precheckAddFactor({ ...base, candidate: def() })).toEqual({ ok: true, alias: "Bias" });
  });

  it("skips column checks while the source columns are still unknown", () => {
    expect(precheckAddFactor({ candidate: def({ input_columns: ["x"] }), members: [], sourceColumns: null })).toEqual({
      ok: true,
      alias: "Bias"
    });
  });

  it("derives the alias from the params; all defaults keep the factor id", () => {
    expect(precheckAddFactor({ ...base, candidate: ema() })).toEqual({ ok: true, alias: "Ema" });
    expect(precheckAddFactor({ ...base, candidate: ema(), params: { warmup: 50000 } })).toEqual({ ok: true, alias: "Ema_20_50000" });
    expect(precheckAddFactor({ ...base, candidate: ema(), params: { span: 50, warmup: 20000 }, alias: "slow" })).toEqual({
      ok: true,
      alias: "slow"
    });
  });

  it("the same factor with different params can be added again; the same params cannot", () => {
    const defaults = member({ factor_id: "Ema", alias: "Ema", params: { span: 20, warmup: 0 }, output_columns: ["ema"] });
    expect(precheckAddFactor({ ...base, candidate: ema(), members: [defaults], params: { warmup: 50000 } })).toEqual({
      ok: true,
      alias: "Ema_20_50000"
    });
    expect(precheckAddFactor({ ...base, candidate: ema(), members: [defaults], params: { span: 20, warmup: 0 } })).toEqual({
      ok: false,
      reason: "别名 Ema 已在该计算任务中",
      fixable: true
    });
    expect(precheckAddFactor({ ...base, candidate: ema(), members: [defaults], params: {}, alias: "again" })).toEqual({
      ok: false,
      reason: "已有相同参数的成员 Ema",
      fixable: true
    });
  });

  it("rejects out-of-range or fractional params and bad aliases", () => {
    expect(precheckAddFactor({ ...base, candidate: ema(), params: { span: 1 } })).toEqual({ ok: false, reason: "参数 span 需在 2 ~ 4825 之间", fixable: true });
    expect(precheckAddFactor({ ...base, candidate: ema(), params: { span: 20.5 } })).toEqual({ ok: false, reason: "参数 span 必须是整数", fixable: true });
    expect(precheckAddFactor({ ...base, candidate: ema(), alias: "bad__alias" }).ok).toBe(false);
    expect(precheckAddFactor({ ...base, candidate: ema(), alias: "1abc" }).ok).toBe(false);
  });

  it("asks for a manual alias when the generated one is too long", () => {
    const long = ema();
    long.factor_id = "ALongFactorIdentifierForTesting";
    const result = precheckAddFactor({ ...base, candidate: long, params: { warmup: 50000 } });
    expect(result.ok).toBe(false);
    expect(result.ok === false && result.reason).toContain("手动指定别名");
  });

  it("result columns of a prefixed alias do not collide with the default member's", () => {
    const defaults = member({ factor_id: "Ema", alias: "Ema", params: { span: 20, warmup: 0 }, output_columns: ["ema"] });
    const result = precheckAddFactor({ ...base, candidate: ema(), members: [defaults], params: { warmup: 50000 } });
    expect(result.ok).toBe(true);
  });
});

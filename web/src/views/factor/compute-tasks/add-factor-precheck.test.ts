import { describe, expect, it } from "vitest";
import type { FactorDef } from "@/api/factor/types";
import { precheckAddFactor } from "./add-factor-precheck";

const def = (patch: Partial<FactorDef> = {}): FactorDef => ({
  factor_id: "Bias",
  name: "Bias",
  factor_type: "timeseries",
  source_hash: "h",
  input_columns: ["close"],
  outputs: ["bias"],
  params_json: "{}",
  lookback_periods: 20,
  allow_partial_universe: false,
  created_at: "",
  updated_at: "",
  ...patch
});

const base = { sourceColumns: ["open", "high", "low", "close", "volume"], members: [] as FactorDef[] };

describe("precheckAddFactor", () => {
  it("candidate already in set is disabled with reason", () => {
    const candidate = def();
    const result = precheckAddFactor({ ...base, candidate, members: [candidate] });
    expect(result).toEqual({ ok: false, reason: "已在该计算任务中" });
  });

  it("missing input column reports the column name", () => {
    const result = precheckAddFactor({ ...base, candidate: def({ input_columns: ["close", "funding_rate"] }) });
    expect(result).toEqual({ ok: false, reason: "输入列 funding_rate 不在源数据集中" });
  });

  it("output colliding with an existing member output is rejected", () => {
    const other = def({ factor_id: "Other", outputs: ["bias"] });
    const result = precheckAddFactor({ ...base, candidate: def({ factor_id: "Bias2" }), members: [other] });
    expect(result).toEqual({ ok: false, reason: "输出列 bias 与因子 Other 的输出列重名" });
  });

  it("output colliding with source column is rejected", () => {
    const result = precheckAddFactor({ ...base, candidate: def({ outputs: ["close"] }) });
    expect(result).toEqual({ ok: false, reason: "输出列 close 与源数据集列重名" });
  });

  it("valid candidate is selectable", () => {
    expect(precheckAddFactor({ ...base, candidate: def() })).toEqual({ ok: true });
  });

  it("skips column checks while the source columns are still unknown", () => {
    expect(precheckAddFactor({ candidate: def({ input_columns: ["x"] }), members: [], sourceColumns: null })).toEqual({
      ok: true
    });
  });
});

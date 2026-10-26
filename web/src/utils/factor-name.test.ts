import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import {
  KIND_VAR,
  PARAMS_COLUMN,
  canonicalParams,
  canonicalValues,
  column,
  columns,
  fixedKey,
  formatValue,
  newSuffix,
  paramMap,
  parseColumn,
  parseMemberKey,
  parseParams,
  parseResultDatasetId,
  parseResultViewId,
  parseTaskId,
  parseValue,
  resultDatasetId,
  resultViewId,
  taskId,
  validateOutputName,
  validateOutputs,
  validateSuffix,
  varKey
} from "./factor-name";

interface Vectors {
  format_value: { value: number; text: string }[];
  fixed_key: {
    name: string;
    factor_id: string;
    decl: { name: string; default: number }[];
    values: Record<string, number>;
    key?: string;
    error?: boolean;
  }[];
  var_key: { factor_id: string; key?: string; error?: boolean }[];
  column: { key: string; output: string; column?: string; error?: boolean }[];
  output_name: { factor_id: string; name: string; ok: boolean }[];
  outputs: { factor_id: string; outputs: string[]; ok: boolean }[];
  parse_column: {
    column: string;
    ok: boolean;
    factor_id?: string;
    kind?: string;
    has_params?: boolean;
    values?: number[];
    output?: string;
  }[];
  canonical_params: { input: Record<string, Record<string, number>>; text?: string; error?: boolean }[];
  canonical_values: { input: Record<string, number>; text?: string; error?: boolean }[];
  id: { suffix: string; task: string; dataset: string; view: string }[];
  suffix_invalid: string[];
  id_parse_invalid: string[];
}

// 与 Go 的 packages/factorname 共用同一份测试向量：两边的拼接、解析与校验必须逐条一致。
const vectors: Vectors = JSON.parse(
  fs.readFileSync(path.resolve(__dirname, "../../../packages/factorname/testdata/vectors.json"), "utf8")
);

describe("shared vectors", () => {
  it("are loaded", () => {
    for (const [name, list] of Object.entries(vectors)) expect(list.length, name).toBeGreaterThan(0);
  });

  it("formatValue / parseValue", () => {
    for (const c of vectors.format_value) {
      expect(formatValue(c.value), String(c.value)).toBe(c.text);
      if (c.value !== 0) expect(parseValue(c.text), c.text).toBe(c.value);
    }
    expect(() => formatValue(Number.NaN)).toThrow();
    expect(() => formatValue(Number.POSITIVE_INFINITY)).toThrow();
  });

  it("fixedKey", () => {
    for (const c of vectors.fixed_key) {
      const decl = c.decl.map(item => ({ name: item.name, default: item.default }));
      if (c.error) expect(() => fixedKey(c.factor_id, decl, c.values), c.name).toThrow();
      else expect(fixedKey(c.factor_id, decl, c.values), c.name).toBe(c.key);
    }
  });

  it("varKey", () => {
    for (const c of vectors.var_key) {
      if (c.error) expect(() => varKey(c.factor_id)).toThrow();
      else expect(varKey(c.factor_id)).toBe(c.key);
    }
  });

  it("column", () => {
    for (const c of vectors.column) {
      if (c.error) expect(() => column(c.key, c.output), `${c.key}/${c.output}`).toThrow();
      else expect(column(c.key, c.output), `${c.key}/${c.output}`).toBe(c.column);
    }
  });

  it("output names and output lists", () => {
    for (const c of vectors.output_name) {
      const check = () => validateOutputName(c.factor_id, c.name);
      if (c.ok) expect(check, c.name).not.toThrow();
      else expect(check, c.name).toThrow();
    }
    for (const c of vectors.outputs) {
      const check = () => validateOutputs(c.factor_id, c.outputs);
      if (c.ok) expect(check, JSON.stringify(c.outputs)).not.toThrow();
      else expect(check, JSON.stringify(c.outputs)).toThrow();
    }
  });

  it("parseColumn", () => {
    for (const c of vectors.parse_column) {
      if (!c.ok) {
        expect(() => parseColumn(c.column), c.column).toThrow();
        continue;
      }
      expect(parseColumn(c.column), c.column).toEqual({
        factorId: c.factor_id,
        kind: c.kind,
        hasParams: c.has_params,
        values: c.values,
        output: c.output
      });
    }
  });

  it("canonical JSON", () => {
    for (const c of vectors.canonical_params) {
      if (c.error) expect(() => canonicalParams(c.input)).toThrow();
      else {
        expect(canonicalParams(c.input)).toBe(c.text);
        expect(parseParams(c.text as string)).toBeTruthy();
      }
    }
    for (const c of vectors.canonical_values) {
      if (c.error) expect(() => canonicalValues(c.input)).toThrow();
      else expect(canonicalValues(c.input)).toBe(c.text);
    }
  });

  it("ids", () => {
    for (const c of vectors.id) {
      expect(taskId(c.suffix)).toBe(c.task);
      expect(resultDatasetId(c.suffix)).toBe(c.dataset);
      expect(resultViewId(c.suffix)).toBe(c.view);
      expect(parseTaskId(c.task)).toBe(c.suffix);
      expect(parseResultDatasetId(c.dataset)).toBe(c.suffix);
      expect(parseResultViewId(c.view)).toBe(c.suffix);
    }
    for (const suffix of vectors.suffix_invalid) expect(() => validateSuffix(suffix), suffix).toThrow();
    for (const id of vectors.id_parse_invalid) expect(() => parseTaskId(id), id).toThrow();
  });
});

describe("helpers", () => {
  const ema = [
    { name: "span", default: 20 },
    { name: "warmup", default: 0 }
  ];

  it("key and column round trip", () => {
    for (const given of [{}, { span: 30 }, { span: 20, warmup: 50000 }, { span: 2.5, warmup: 0 }]) {
      const key = fixedKey("ema", ema, given);
      const parsed = parseMemberKey(key);
      expect(parsed.factorId).toBe("ema");
      const params = paramMap(parsed, ema) as Record<string, number>;
      expect(params.span).toBe((given as Record<string, number>).span ?? 20);
      expect(params.warmup).toBe((given as Record<string, number>).warmup ?? 0);
    }
    expect(columns("boll", "boll__30", ["mid", "up", "low", "pctb"])).toEqual([
      "boll__30__mid",
      "boll__30__up",
      "boll__30__low",
      "boll__30__pctb"
    ]);
    expect(columns("ema", "ema__20_50000", ["ema"])).toEqual(["ema__20_50000"]);
  });

  it("paramMap checks the declared count and var members have no values", () => {
    expect(() => paramMap(parseMemberKey("ema__20_50000"), [{ name: "span", default: 20 }])).toThrow();
    const variable = parseMemberKey("ma_fast__var");
    expect(variable.kind).toBe(KIND_VAR);
    expect(paramMap(variable, [{ name: "window", default: 5 }])).toBeNull();
  });

  it("__params is not a factor column", () => {
    expect(() => parseColumn(PARAMS_COLUMN)).toThrow();
  });

  it("newSuffix is valid", () => {
    const seen = new Set<string>();
    for (let i = 0; i < 50; i++) {
      const suffix = newSuffix();
      expect(suffix).toHaveLength(8);
      expect(() => validateSuffix(suffix)).not.toThrow();
      seen.add(suffix);
    }
    expect(seen.size).toBeGreaterThan(45);
  });
});

import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { completeMemberParams, deriveMemberAlias, fixedParamsText, formatParamValue } from "./member-params";

interface Vector {
  name: string;
  factor_id: string;
  params: Record<string, number>;
  alias: string;
  error?: string;
  decl: { name: string; default: number }[];
}

// 与服务端共用同一份测试向量：别名预览必须与服务端生成的一致。
const vectors: Vector[] = JSON.parse(
  fs.readFileSync(path.resolve(__dirname, "../../../../../modules/factor/testdata/alias_vectors.json"), "utf8")
);

describe("member alias derivation", () => {
  it("has the shared vectors", () => {
    expect(vectors.length).toBeGreaterThan(5);
  });

  for (const vector of vectors) {
    it(vector.name, () => {
      const decls = vector.decl.map(item => ({ name: item.name, default_value: item.default }));
      const derived = deriveMemberAlias(vector.factor_id, decls, vector.params);
      if (vector.error) {
        expect(derived.tooLong).toBe(true);
      } else {
        expect(derived).toEqual({ alias: vector.alias, tooLong: false });
      }
    });
  }

  it("formats values without exponents, with p for the decimal point and n for the sign", () => {
    expect(formatParamValue(20)).toBe("20");
    expect(formatParamValue(0.5)).toBe("0p5");
    expect(formatParamValue(-1)).toBe("n1");
    expect(formatParamValue(-0.25)).toBe("n0p25");
    expect(formatParamValue(-0)).toBe("0");
    expect(formatParamValue(1e-7)).toBe("0p0000001");
    expect(formatParamValue(123456789)).toBe("123456789");
    expect(formatParamValue(1e21)).not.toMatch(/e|\+/);
  });

  it("completes params with the declared defaults", () => {
    const decls = [
      { name: "span", default_value: 20 },
      { name: "warmup", default_value: 0 }
    ];
    expect(completeMemberParams(decls, { warmup: 50000 })).toEqual({ span: 20, warmup: 50000 });
    expect(completeMemberParams(decls)).toEqual({ span: 20, warmup: 0 });
  });

  it("renders fixed params sorted by name", () => {
    expect(fixedParamsText({ warmup: 50000, span: 20 })).toBe("span=20, warmup=50000");
    expect(fixedParamsText({})).toBe("");
    expect(fixedParamsText(undefined)).toBe("");
  });
});

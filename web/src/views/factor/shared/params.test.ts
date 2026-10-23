import { describe, expect, it } from "vitest";
import type { FactorParamDecl, ParamInterval, ParamValue } from "@/api/factor/types";
import { categoryName, describeParam, describeParams, paramsSourceText, paramTimeline, paramValueText } from "./params";

const window: FactorParamDecl = { name: "window", type: "int", min: 2, max: 120, default_value: 20 };

describe("factor params helpers", () => {
  it("describes declared parameters with type, range and default", () => {
    expect(describeParam(window)).toBe("window（整数，2 ~ 120，默认 20）");
    expect(describeParam({ name: "k", type: "float", min: 0.5, max: 4, default_value: 2 })).toBe("k（小数，0.5 ~ 4，默认 2）");
    // 后端 ProtoJSON 会省略值为 0 的字段：min、default 为 0 时不能显示 undefined。
    expect(describeParam({ name: "z", type: "float", max: 5 } as FactorParamDecl)).toBe("z（小数，0 ~ 5，默认 0）");
    expect(paramValueText({} as ParamValue)).toBe("0");
    expect(describeParams([window, { name: "k", type: "float", min: 0.5, max: 4, default_value: 2 }])).toContain("；");
    expect(describeParams([])).toContain("无");
    expect(describeParams(undefined)).toContain("无");
  });

  it("falls back to the uncategorized label", () => {
    const categories = [{ category_id: "momentum", name: "动量类" }];
    expect(categoryName(categories, "momentum")).toBe("动量类");
    expect(categoryName(categories, "")).toBe("未分类");
    expect(categoryName(categories, undefined)).toBe("未分类");
    expect(categoryName(categories, "gone")).toBe("gone");
  });

  it("names the parameter sources", () => {
    expect(paramsSourceText("requested")).toBe("提交生效");
    expect(paramsSourceText("carried_offline")).toBe("离线沿用");
    expect(paramsSourceText("other")).toBe("other");
  });

  it("mentions per-subject overrides in a value", () => {
    expect(paramValueText({ default_value: 20 })).toBe("20");
    expect(paramValueText({ default_value: 20, overrides: { BTC: 10, ETH: 30 } })).toBe("20（2 个标的覆盖）");
  });

  it("builds a newest-first timeline with the period count of each interval", () => {
    const intervals: ParamInterval[] = [
      {
        member_alias: "fast",
        start_period: "0",
        end_period: "240",
        params: { window: { default_value: 20 } },
        source: "default",
        snapshot_hash: "a"
      },
      {
        member_alias: "fast",
        start_period: 300,
        end_period: 300,
        params: { window: { default_value: 30, overrides: { BTC: 10 } } },
        source: "requested",
        snapshot_hash: "b"
      },
      {
        member_alias: "slow",
        start_period: 300,
        end_period: 360,
        params: { window: { default_value: 60 } },
        source: "carried",
        snapshot_hash: "c"
      }
    ];
    const rows = paramTimeline(intervals, 60);
    expect(rows.map(row => `${row.member}@${row.start}`)).toEqual(["fast@300", "slow@300", "fast@0"]);
    expect(rows[0]).toMatchObject({ periods: 1, sourceText: "提交生效", params: "window=30（1 个标的覆盖）" });
    expect(rows[1].periods).toBe(2);
    expect(rows[2].periods).toBe(5);
  });
});

import { describe, expect, it } from "vitest";
import {
  mergeAttributes,
  subjectCurrency,
  subjectMarket,
  subjectType,
  tagCron,
  tagMarketType,
  tagSource,
  tagTimezone
} from "./catalog-attributes";

describe("标签与标的属性读取", () => {
  it("从标签属性读取来源、市场类型与同步配置，缺省取每小时与 UTC", () => {
    const tag = {
      attributes: { "sync.source": " binance ", market_type: "spot", "sync.cron": "5 * * * *", "sync.timezone": "Asia/Shanghai" }
    };
    expect([tagSource(tag), tagMarketType(tag), tagCron(tag), tagTimezone(tag)]).toEqual([
      "binance",
      "spot",
      "5 * * * *",
      "Asia/Shanghai"
    ]);
    expect([tagSource({}), tagMarketType({}), tagCron({}), tagTimezone({})]).toEqual(["", "", "0 * * * *", "UTC"]);
  });

  it("从标的属性读取类型、市场与币种，没有属性时为空串", () => {
    const subject = { attributes: { subject_type: "crypto_pair", market: "spot", currency: "USDT", base: "BTC" } };
    expect([subjectType(subject), subjectMarket(subject), subjectCurrency(subject)]).toEqual(["crypto_pair", "spot", "USDT"]);
    expect([subjectType(undefined), subjectMarket({}), subjectCurrency({})]).toEqual(["", "", ""]);
  });

  it("合并属性时空值删除键、其余键保留，且不修改原对象", () => {
    const base = { a: "1", b: "2" };
    expect(mergeAttributes(base, { a: " 9 ", b: "  ", c: "3" })).toEqual({ a: "9", c: "3" });
    expect(base).toEqual({ a: "1", b: "2" });
    expect(mergeAttributes(undefined, { x: "y" })).toEqual({ x: "y" });
  });
});

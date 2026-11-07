import { describe, expect, it } from "vitest";
import {
  datasetMarketText,
  isUnifiedMarketType,
  marketLabel,
  seriesTagDisplay,
  seriesTagExplicitMarket,
  seriesTagMarketText
} from "./series-market";

describe("series tag market", () => {
  it("parses the market segment of the production tags", () => {
    expect(seriesTagExplicitMarket("venue:binance|market:spot|source:spot_http")).toBe("spot");
    expect(seriesTagExplicitMarket("venue:binance|market:swap|source:swap_http")).toBe("swap");
    expect(seriesTagExplicitMarket("venue:okx|market:SWAP")).toBe("swap");
  });

  it("没有 market 段的 tag 不认作任何市场，详情里说明没写市场", () => {
    for (const tag of ["venue:binance", "default", "", undefined, "venue:binance|market:"]) {
      expect(seriesTagExplicitMarket(tag)).toBeNull();
      expect(seriesTagMarketText(tag)).toBe("序列标签里没有写市场");
    }
    expect(seriesTagMarketText("venue:binance|market:swap")).toBe("永续");
  });

  it("labels markets in Chinese", () => {
    expect(seriesTagDisplay("venue:binance|market:swap|source:swap_http")).toBe("永续");
    expect(seriesTagDisplay("venue:binance|market:spot|source:spot_http")).toBe("现货");
    expect(seriesTagDisplay("default")).toBe("default");
    expect(seriesTagDisplay("")).toBe("-");
    expect(marketLabel("futures")).toBe("futures");
    expect(marketLabel("")).toBe("-");
  });

  it("describes unified datasets", () => {
    expect(isUnifiedMarketType(" Multi ")).toBe(true);
    expect(isUnifiedMarketType("spot")).toBe(false);
    expect(datasetMarketText("multi")).toBe("统一数据集（现货、永续）");
    expect(datasetMarketText("swap")).toBe("永续");
  });
});

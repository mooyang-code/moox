import { describe, expect, it } from "vitest";
import { datasetMarketText, isUnifiedMarketType, marketLabel, seriesTagDisplay, seriesTagMarket } from "./series-market";

describe("series tag market", () => {
  it("parses the market segment of the production tags", () => {
    expect(seriesTagMarket("venue:binance|market:spot|source:spot_http")).toBe("spot");
    expect(seriesTagMarket("venue:binance|market:swap|source:swap_http")).toBe("swap");
    expect(seriesTagMarket("venue:okx|market:SWAP")).toBe("swap");
  });

  it("treats tags without a market segment as spot", () => {
    expect(seriesTagMarket("venue:binance")).toBe("spot");
    expect(seriesTagMarket("default")).toBe("spot");
    expect(seriesTagMarket("")).toBe("spot");
    expect(seriesTagMarket(undefined)).toBe("spot");
    expect(seriesTagMarket("venue:binance|market:")).toBe("spot");
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

import { describe, expect, it } from "vitest";
import { buildExchangeExtra, parseExchangeExtra } from "./exchange-extra";

describe("交易所密钥 extra_config 结构化读写", () => {
  it("解析生产/测试网、只读、脱敏 passphrase 与其他扩展键", () => {
    expect(parseExchangeExtra('{"environment":"testnet","readonly":"true","passphrase":"******","note":"x"}')).toEqual({
      environment: "testnet",
      readonly: true,
      passphrase: "",
      hasPassphrase: true,
      other: { note: "x" }
    });
    expect(parseExchangeExtra('{"readonly":true}')).toEqual({
      environment: "production",
      readonly: true,
      passphrase: "",
      hasPassphrase: false,
      other: {}
    });
    expect(parseExchangeExtra(undefined).environment).toBe("production");
  });

  it("生成全字符串值的 JSON（兼容后端 map[string]string）", () => {
    const binanceReadonly = JSON.parse(
      buildExchangeExtra({
        provider: "binance",
        environment: "production",
        readonly: true,
        passphrase: "ignored-for-binance",
        hasPassphrase: false
      })
    );
    expect(binanceReadonly).toEqual({ environment: "production", readonly: "true" });
    expect(typeof binanceReadonly.readonly).toBe("string");

    const okxKeep = JSON.parse(
      buildExchangeExtra({
        provider: "okx",
        environment: "testnet",
        readonly: false,
        passphrase: "",
        hasPassphrase: true,
        other: { tag: "demo" }
      })
    );
    expect(okxKeep).toEqual({ tag: "demo", environment: "testnet", passphrase: "******" });

    const okxUpdate = JSON.parse(
      buildExchangeExtra({
        provider: "okx",
        environment: "production",
        readonly: false,
        passphrase: "new-pass",
        hasPassphrase: true
      })
    );
    expect(okxUpdate).toEqual({ environment: "production", passphrase: "new-pass" });
  });
});

import { describe, expect, it } from "vitest";
import { buildExchangeExtra, exchangeEnvironmentDisplay, parseExchangeExtra } from "./exchange-extra";

describe("交易所密钥 extra_config 结构化读写", () => {
  it("解析生产/测试网、只读、脱敏 passphrase 与其他扩展键；缺失或非法环境保持未标注", () => {
    expect(parseExchangeExtra('{"environment":"testnet","readonly":"true","passphrase":"******","note":"x"}')).toEqual({
      environment: "testnet",
      readonly: true,
      passphrase: "",
      hasPassphrase: true,
      other: { note: "x" }
    });
    expect(parseExchangeExtra('{"environment":"PRODUCTION"}').environment).toBe("production");
    expect(parseExchangeExtra('{"readonly":true}')).toEqual({
      environment: "",
      readonly: true,
      passphrase: "",
      hasPassphrase: false,
      other: {}
    });
    expect(parseExchangeExtra('{"environment":"unknown"}').environment).toBe("");
    expect(parseExchangeExtra(undefined).environment).toBe("");
  });

  it("列表展示环境标签：旧密钥缺省环境显示未标注（按生产处理）", () => {
    expect(exchangeEnvironmentDisplay('{"environment":"production"}')).toEqual({ label: "生产", color: "green" });
    expect(exchangeEnvironmentDisplay('{"environment":"testnet"}')).toEqual({ label: "测试网", color: "arcoblue" });
    expect(exchangeEnvironmentDisplay('{"readonly":"true"}')).toEqual({ label: "未标注（按生产处理）", color: "orange" });
    expect(exchangeEnvironmentDisplay(undefined)).toEqual({ label: "未标注（按生产处理）", color: "orange" });
  });

  it("生成全字符串值的 JSON（兼容后端 map[string]string），未选择环境时拒绝静默写成 production", () => {
    expect(() =>
      buildExchangeExtra({
        provider: "binance",
        environment: "",
        readonly: false,
        passphrase: "",
        hasPassphrase: false
      })
    ).toThrow("请明确选择交易所密钥所属环境");

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

import { describe, expect, it, vi } from "vitest";
import { loadExchangeSecrets } from "./exchange-secrets";

vi.mock("@/api/admin/secret", () => ({
  listSecrets: vi.fn(async () => ({
    secrets: [
      {
        secret_id: "sec_prod_trade",
        name: "币安实盘",
        category: "exchange",
        provider: "binance",
        status: "active",
        extra_config: '{"environment":"production"}'
      },
      {
        secret_id: "sec_prod_ro",
        name: "币安只读",
        category: "exchange",
        provider: "binance",
        status: "active",
        extra_config: '{"environment":"production","readonly":"true"}'
      },
      {
        secret_id: "sec_test_trade",
        name: "币安测试网",
        category: "exchange",
        provider: "binance",
        status: "active",
        extra_config: '{"environment":"testnet"}'
      },
      {
        secret_id: "sec_legacy_no_env",
        name: "历史导入无环境",
        category: "exchange",
        provider: "binance",
        status: "active",
        extra_config: "{}"
      },
      {
        secret_id: "sec_okx_prod",
        name: "OKX 实盘",
        category: "exchange",
        provider: "okx",
        status: "active",
        extra_config: '{"environment":"production"}'
      }
    ]
  }))
}));

describe("交易所密钥候选筛选", () => {
  it("排除只读密钥并按 extra_config.environment 过滤，保留未标环境的旧密钥", async () => {
    const prod = await loadExchangeSecrets("binance", 2);
    expect(prod.map(item => item.secretId)).toEqual(["sec_prod_trade", "sec_legacy_no_env"]);

    const testnet = await loadExchangeSecrets("binance", 1);
    expect(testnet.map(item => item.secretId)).toEqual(["sec_test_trade", "sec_legacy_no_env"]);

    const okx = await loadExchangeSecrets("okx", 2);
    expect(okx.map(item => item.secretId)).toEqual(["sec_okx_prod"]);
  });
});

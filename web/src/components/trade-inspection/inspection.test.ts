import { describe, expect, it } from "vitest";
import {
  accountModeLabel,
  fromInspection,
  fromStoredInspection,
  inspectionFailureNote,
  inspectionRows,
  inspectionVerdict
} from "./inspection";
import {
  canTestConnection,
  exchangeFromProvider,
  inspectionRequest,
  isReadonlySecret,
  secretEnvironmentOf
} from "./inspection-request";

describe("检测结果的展示模型", () => {
  it("RPC 返回按枚举转市场、读取阻断项", () => {
    const view = fromInspection({
      account_mode: "BINANCE_STANDARD",
      can_withdraw: true,
      can_trade_spot: true,
      available_markets: [1, 2],
      blockers: [{ code: "withdraw_permission", message: "密钥带有提现权限" }],
      inspected_at: "1790000000000"
    });
    expect(view.availableMarkets).toEqual(["SPOT", "SWAP"]);
    expect(view.inspectedAt).toBe(1790000000000);
    expect(inspectionVerdict(view)).toBe("blocked");
  });

  it("账户里存的检测结果按后端存储的字段解析，零时间当作没有检测时间", () => {
    const stored = JSON.stringify({
      account_mode: "OKX_UNIFIED",
      okx_acct_lv: "1",
      position_mode: "ONE_WAY",
      available_markets: ["SPOT"],
      can_trade_spot: true,
      inspected_at: "0001-01-01T00:00:00Z",
      consecutive_failures: 6,
      last_failure: "HTTP 503"
    });
    const view = fromStoredInspection(stored);
    expect(view).not.toBeNull();
    expect(view?.inspectedAt).toBe(0);
    expect(view?.availableMarkets).toEqual(["SPOT"]);
    expect(inspectionFailureNote(view!)).toContain("最近 6 次复查没有检测成功");
    expect(inspectionFailureNote(view!)).toContain("HTTP 503");
  });

  it("没检测过的账户（空串、空对象、坏 JSON）没有检测结果", () => {
    expect(fromStoredInspection("")).toBeNull();
    expect(fromStoredInspection("{}")).toBeNull();
    expect(fromStoredInspection("not json")).toBeNull();
    expect(fromStoredInspection(undefined)).toBeNull();
  });

  it("明细行：普通账户显示万向划转权限，OKX 显示账户层级，测试网只说明跳过", () => {
    const standard = fromInspection({ account_mode: "BINANCE_STANDARD", universal_transfer: true, available_markets: [1] });
    expect(inspectionRows(standard).map(row => row.label)).toContain("万向划转权限");
    const okx = fromInspection({ account_mode: "OKX_UNIFIED", okx_acct_lv: "2", position_mode: "HEDGE" });
    const okxRows = Object.fromEntries(inspectionRows(okx).map(row => [row.label, row.value]));
    expect(okxRows["OKX 账户层级"]).toBe("合约模式");
    expect(okxRows["合约持仓模式"]).toBe("双向持仓");
    expect(okxRows["万向划转权限"]).toBeUndefined();
    const skipped = fromInspection({ account_mode: "BINANCE_STANDARD", skipped: true });
    expect(inspectionVerdict(skipped)).toBe("skipped");
    expect(inspectionRows(skipped)).toHaveLength(2);
  });

  it("账户类型的中文名", () => {
    expect(accountModeLabel("BINANCE_PORTFOLIO_MARGIN")).toContain("统一账户");
    expect(accountModeLabel("")).toBe("未检测");
  });
});

describe("测试连接请求", () => {
  it("提供方对应交易所枚举，其他提供方不能检测", () => {
    expect(exchangeFromProvider("Binance")).toBe(1);
    expect(exchangeFromProvider("okx")).toBe(2);
    expect(exchangeFromProvider("mysql")).toBe(0);
    expect(() => inspectionRequest({ provider: "mysql", secretId: "s", environment: 2, markets: [] })).toThrow();
  });

  it("不勾选市场时不传 markets，勾选后按固定顺序传", () => {
    expect(inspectionRequest({ provider: "binance", secretId: "s-1", environment: 2, markets: [] })).toEqual({
      exchange: 1,
      environment: 2,
      credential_secret_id: "s-1"
    });
    expect(inspectionRequest({ provider: "okx", secretId: "s-2", environment: 1, markets: [2, 1] }).markets).toEqual([1, 2]);
  });

  it("只有启用的币安、OKX 交易所密钥可以测试连接", () => {
    expect(canTestConnection({ category: "exchange", provider: "binance", status: "active" })).toBe(true);
    expect(canTestConnection({ category: "exchange", provider: "binance", status: "inactive" })).toBe(false);
    expect(canTestConnection({ category: "database", provider: "binance", status: "active" })).toBe(false);
    expect(canTestConnection({ category: "exchange", provider: "kraken", status: "active" })).toBe(false);
  });
});

describe("密钥 ID 的环境段与只读标记", () => {
  it("exchange-<交易所>-<环境>-<名称> 读出环境，其他格式看不出来", () => {
    expect(secretEnvironmentOf("exchange-binance-testnet-main")).toBe(1);
    expect(secretEnvironmentOf("exchange-okx-production-a")).toBe(2);
    expect(secretEnvironmentOf("sec_8f2c")).toBe(0);
    expect(secretEnvironmentOf(undefined)).toBe(0);
  });

  it("extra_config 里 readonly 为 true 的是只读密钥，解析不了按普通密钥", () => {
    expect(isReadonlySecret('{"readonly":"true"}')).toBe(true);
    expect(isReadonlySecret('{"passphrase":"***"}')).toBe(false);
    expect(isReadonlySecret("not json")).toBe(false);
    expect(isReadonlySecret(undefined)).toBe(false);
  });
});

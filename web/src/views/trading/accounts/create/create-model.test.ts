import { describe, expect, it } from "vitest";
import { buildLiveRequest, buildPaperRequest, createDefaultForm, defaultFees, toggleMarket, validateStep } from "./create-model";

describe("新建账户", () => {
  it("1A 期间测试网和生产环境不能选", () => {
    const form = createDefaultForm();
    expect(validateStep(form, 0)).toBe("");
    expect(validateStep({ ...form, kind: "testnet" }, 0)).not.toBe("");
    expect(validateStep({ ...form, kind: "prod" }, 0)).not.toBe("");
  });

  it("模拟盘请求带市场集合，永续固定全仓，新建组合时组合名称缺省取账户名", () => {
    const form = { ...createDefaultForm(), name: " 演示 ", markets: [1, 2] as (1 | 2)[], controlMode: 2 as const };
    expect(buildPaperRequest(form)).toMatchObject({
      account_name: "演示",
      logical_account_name: "演示",
      control_mode: 2,
      markets: [1, 2],
      margin_mode: "CROSS",
      settlement_asset: "USDT"
    });
    expect(buildPaperRequest({ ...form, logicalName: "组合一" }).logical_account_name).toBe("组合一");
    expect(buildPaperRequest(form).logical_account_id).toBeUndefined();
  });

  it("加入已有组合时带组合编号、不带组合名称，由后端在同一个事务里加入；只开现货不设保证金模式", () => {
    const form = { ...createDefaultForm(), name: "a", markets: [1] as 1[], join: "exist" as const, existingLogicalId: "l" };
    const request = buildPaperRequest(form);
    expect(request.logical_account_name).toBe("");
    expect(request.logical_account_id).toBe("l");
    expect(request.margin_mode).toBe("");
  });

  it("实盘请求带环境和密钥", () => {
    const form = { ...createDefaultForm("prod"), name: "k", secretId: "sec-1", markets: [2] as 2[] };
    expect(buildLiveRequest(form)).toMatchObject({
      markets: [2],
      live: { environment: 2, credential_secret_id: "sec-1" },
      margin_mode: "CROSS"
    });
    expect(buildLiveRequest({ ...form, kind: "testnet" }).live.environment).toBe(1);
  });

  it("逐步校验：账户名、市场、模拟参数、生产环境确认、选组合", () => {
    const form = createDefaultForm();
    expect(validateStep(form, 1)).toBe("请输入账户名称");
    expect(validateStep({ ...form, name: "a" }, 1)).toBe("");
    expect(validateStep({ ...form, name: "a", kind: "testnet" }, 1)).toBe("请选择交易所密钥");
    expect(validateStep({ ...form, markets: [] }, 2)).toBe("至少开通一个市场");
    expect(validateStep({ ...form, initialBalance: "0" }, 2)).toContain("初始资金");
    expect(validateStep({ ...form, slippageBps: "10000" }, 2)).toContain("滑点");
    expect(validateStep({ ...form, settlementAsset: "BUSD" }, 2)).toContain("USDT");
    expect(validateStep({ ...form, kind: "prod", ack: false }, 2)).toContain("提现权限");
    expect(validateStep({ ...form, join: "exist" }, 3)).toContain("选择");
    expect(validateStep({ ...form, join: "new" }, 3)).toBe("");
  });

  it("市场勾选保持固定顺序；只有现货时用现货费率", () => {
    expect(toggleMarket([2], 1, true)).toEqual([1, 2]);
    expect(toggleMarket([1, 2], 2, false)).toEqual([1]);
    expect(defaultFees([1])).toEqual({ maker: "0.001", taker: "0.001" });
    expect(defaultFees([1, 2]).taker).toBe("0.0005");
  });
});

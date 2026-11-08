import { describe, expect, it, vi } from "vitest";
import { TradeResponseError } from "@/api/trade/http";
import {
  applyExchangeDefaults,
  buildLiveRequest,
  buildPaperRequest,
  createDefaultForm,
  defaultFees,
  restrictMarkets,
  retryWhileSessionStarting,
  toggleMarket,
  validateStep
} from "./create-model";

describe("A 股模拟账户", () => {
  it("选 A 股后市场固定为股票、人民币结算、默认 5000 万元、佣金 0.025%，换回加密货币恢复默认", () => {
    const form = createDefaultForm();
    form.exchange = 3;
    applyExchangeDefaults(form);
    expect(form.markets).toEqual([3]);
    expect(form.settlementAsset).toBe("CNY");
    expect(form.initialBalance).toBe("50000000");
    expect(form.takerFeeRate).toBe("0.00025");
    expect(defaultFees([3])).toEqual({ maker: "0.00025", taker: "0.00025" });
    form.exchange = 1;
    applyExchangeDefaults(form);
    expect(form.markets).toEqual([2]);
    expect(form.settlementAsset).toBe("USDT");
    expect(form.initialBalance).toBe("100000");
  });

  it("只有一个佣金比例：请求里挂单、吃单相同", () => {
    const form = { ...createDefaultForm(), name: "a 股", exchange: 3 as const };
    applyExchangeDefaults(form);
    form.takerFeeRate = "0.0003";
    expect(buildPaperRequest(form)).toMatchObject({
      exchange: 3,
      markets: [3],
      settlement_asset: "CNY",
      margin_mode: "",
      maker_fee_rate: "0.0003",
      taker_fee_rate: "0.0003"
    });
    expect(validateStep(form, 2)).toBe("");
  });

  it("A 股只开股票、只能模拟盘；加密货币场所不能开股票", () => {
    const form = { ...createDefaultForm(), name: "a", exchange: 3 as const };
    applyExchangeDefaults(form);
    expect(validateStep({ ...form, markets: [1] }, 2)).toContain("只能开通股票");
    expect(validateStep({ ...form, settlementAsset: "USDT" }, 2)).toContain("CNY");
    expect(validateStep({ ...form, kind: "testnet", secretId: "s" }, 1)).toContain("QMT");
    expect(validateStep({ ...createDefaultForm(), name: "b", markets: [3] }, 2)).toContain("只能在 A 股场所开通");
  });
});

describe("新建账户", () => {
  it("模拟盘、测试网、生产环境都能选", () => {
    const form = createDefaultForm();
    for (const kind of ["paper", "testnet", "prod"] as const) {
      expect(validateStep({ ...form, kind }, 0)).toBe("");
    }
  });

  it("实盘按检测出的可开通市场收窄勾选，没有限制时原样保留", () => {
    expect(restrictMarkets([1, 2], [])).toEqual([1, 2]);
    expect(restrictMarkets([1, 2], ["SPOT"])).toEqual([1]);
    expect(restrictMarkets([2], ["SPOT"])).toEqual([1]);
    expect(restrictMarkets([1], ["SPOT", "SWAP"])).toEqual([1]);
  });

  it("币安测试网只能开通一个市场，OKX 不受限", () => {
    const base = { ...createDefaultForm(), name: "a", secretId: "s", kind: "testnet" as const, markets: [1, 2] as (1 | 2)[] };
    expect(validateStep(base, 2)).toContain("只能开通一个市场");
    expect(validateStep({ ...base, exchange: 2 }, 2)).toBe("");
    expect(validateStep({ ...base, markets: [2] }, 2)).toBe("");
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

  it("加入组合只在会话还没建起来（TRADE_SESSION_NOT_READY）时重试，其他错误直接抛出", async () => {
    const sleep = () => Promise.resolve();
    const notReady = (code: number | string) =>
      new TradeResponseError({ ret_info: { code, msg: "交易账户会话还没有启动，请稍后重试" } });
    const call = vi.fn().mockRejectedValueOnce(notReady(19)).mockResolvedValueOnce("ok");
    await expect(retryWhileSessionStarting(call, { sleep })).resolves.toBe("ok");
    expect(call).toHaveBeenCalledTimes(2);

    const byName = vi.fn().mockRejectedValueOnce(notReady("TRADE_SESSION_NOT_READY")).mockResolvedValueOnce("ok");
    await expect(retryWhileSessionStarting(byName, { sleep })).resolves.toBe("ok");

    // 消息文字相同但错误码不是 TRADE_SESSION_NOT_READY 时不重试：只认错误码
    const sameText = vi.fn().mockRejectedValue(notReady(4));
    await expect(retryWhileSessionStarting(sameText, { sleep })).rejects.toThrow("会话还没有启动");
    expect(sameText).toHaveBeenCalledTimes(1);

    const other = vi.fn().mockRejectedValue(new Error("先暂停自动执行"));
    await expect(retryWhileSessionStarting(other, { sleep })).rejects.toThrow("先暂停自动执行");
    expect(other).toHaveBeenCalledTimes(1);

    const never = vi.fn().mockRejectedValue(notReady(19));
    await expect(retryWhileSessionStarting(never, { sleep, attempts: 3 })).rejects.toThrow("会话还没有启动");
    expect(never).toHaveBeenCalledTimes(3);
  });
});

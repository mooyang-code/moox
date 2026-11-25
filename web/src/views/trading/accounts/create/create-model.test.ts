import { describe, expect, it, vi } from "vitest";
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
import { RequestError } from "@/api/http";

// 交易服务的业务失败：消息就是 ret_info.msg，响应体原样带上
const tradeFailure = (response: { ret_info: { code: number | string; msg: string } } & Record<string, unknown>) =>
  new RequestError(response.ret_info.msg, response);

describe("A 股模拟账户", () => {
  it("选 A 股后市场固定为股票、人民币结算、默认 5000 万元、佣金 0.025%，换回加密货币恢复默认", () => {
    const form = createDefaultForm();
    form.exchange = "ashare";
    applyExchangeDefaults(form);
    expect(form.markets).toEqual(["equity"]);
    expect(form.settlementAsset).toBe("CNY");
    expect(form.initialBalance).toBe("50000000");
    expect(form.takerFeeRate).toBe("0.00025");
    expect(defaultFees(["equity"])).toEqual({ maker: "0.00025", taker: "0.00025" });
    form.exchange = "binance";
    applyExchangeDefaults(form);
    expect(form.markets).toEqual(["swap"]);
    expect(form.settlementAsset).toBe("USDT");
    expect(form.initialBalance).toBe("100000");
  });

  it("只有一个佣金比例：请求里挂单、吃单相同", () => {
    const form = { ...createDefaultForm(), name: "a 股", exchange: "ashare" as const };
    applyExchangeDefaults(form);
    form.takerFeeRate = "0.0003";
    expect(buildPaperRequest(form)).toMatchObject({
      exchange: "ashare",
      markets: ["equity"],
      settlement_asset: "CNY",
      margin_mode: "",
      maker_fee_rate: "0.0003",
      taker_fee_rate: "0.0003"
    });
    expect(validateStep(form, 2)).toBe("");
  });

  it("A 股只开股票、只能模拟盘；加密货币场所不能开股票", () => {
    const form = { ...createDefaultForm(), name: "a", exchange: "ashare" as const };
    applyExchangeDefaults(form);
    expect(validateStep({ ...form, markets: ["spot"] }, 2)).toContain("只能开通股票");
    expect(validateStep({ ...form, settlementAsset: "USDT" }, 2)).toContain("CNY");
    expect(validateStep({ ...form, kind: "testnet", secretId: "s" }, 1)).toContain("QMT");
    expect(validateStep({ ...createDefaultForm(), name: "b", markets: ["equity"] }, 2)).toContain("只能在 A 股、港股场所开通");
  });
});

describe("港股模拟账户", () => {
  it("选港股后市场固定为股票、港元结算、默认 1000 万港元、佣金 0.03%，换成 A 股、加密货币各用各的默认值", () => {
    const form = createDefaultForm();
    form.exchange = "hkex";
    applyExchangeDefaults(form);
    expect(form.markets).toEqual(["equity"]);
    expect(form.settlementAsset).toBe("HKD");
    expect(form.initialBalance).toBe("10000000");
    expect(form.makerFeeRate).toBe("0.0003");
    expect(form.takerFeeRate).toBe("0.0003");
    expect(defaultFees(["equity"], "hkex")).toEqual({ maker: "0.0003", taker: "0.0003" });
    form.exchange = "ashare";
    applyExchangeDefaults(form);
    expect(form.settlementAsset).toBe("CNY");
    expect(form.takerFeeRate).toBe("0.00025");
    form.exchange = "okx";
    applyExchangeDefaults(form);
    expect(form.markets).toEqual(["swap"]);
    expect(form.settlementAsset).toBe("USDT");
  });

  it("请求带港股场所、股票市场、HKD，挂单、吃单用同一个佣金", () => {
    const form = { ...createDefaultForm(), name: "港股模拟", exchange: "hkex" as const };
    applyExchangeDefaults(form);
    expect(validateStep(form, 1)).toBe("");
    expect(validateStep(form, 2)).toBe("");
    expect(buildPaperRequest(form)).toMatchObject({
      exchange: "hkex",
      markets: ["equity"],
      settlement_asset: "HKD",
      margin_mode: "",
      initial_balance: "10000000",
      maker_fee_rate: "0.0003",
      taker_fee_rate: "0.0003"
    });
  });

  it("港股只开股票、按 HKD 结算、只能模拟盘", () => {
    const form = { ...createDefaultForm(), name: "hk", exchange: "hkex" as const };
    applyExchangeDefaults(form);
    expect(validateStep({ ...form, markets: ["spot"] }, 2)).toContain("港股只能开通股票");
    expect(validateStep({ ...form, settlementAsset: "CNY" }, 2)).toContain("港股按港元（HKD）结算");
    expect(validateStep({ ...form, kind: "prod", secretId: "s" }, 1)).toContain("港股只支持模拟盘");
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
    expect(restrictMarkets(["spot", "swap"], [])).toEqual(["spot", "swap"]);
    expect(restrictMarkets(["spot", "swap"], ["spot"])).toEqual(["spot"]);
    expect(restrictMarkets(["swap"], ["spot"])).toEqual(["spot"]);
    expect(restrictMarkets(["spot"], ["spot", "swap"])).toEqual(["spot"]);
  });

  it("币安测试网只能开通一个市场，OKX 不受限", () => {
    const base = {
      ...createDefaultForm(),
      name: "a",
      secretId: "s",
      kind: "testnet" as const,
      markets: ["spot", "swap"] as (1 | 2)[]
    };
    expect(validateStep(base, 2)).toContain("只能开通一个市场");
    expect(validateStep({ ...base, exchange: "okx" }, 2)).toBe("");
    expect(validateStep({ ...base, markets: ["swap"] }, 2)).toBe("");
  });

  it("模拟盘请求带市场集合，永续固定全仓，新建组合时组合名称缺省取账户名", () => {
    const form = { ...createDefaultForm(), name: " 演示 ", markets: ["spot", "swap"] as (1 | 2)[], controlMode: 2 as const };
    expect(buildPaperRequest(form)).toMatchObject({
      account_name: "演示",
      logical_account_name: "演示",
      control_mode: 2,
      markets: ["spot", "swap"],
      margin_mode: "CROSS",
      settlement_asset: "USDT"
    });
    expect(buildPaperRequest({ ...form, logicalName: "组合一" }).logical_account_name).toBe("组合一");
    expect(buildPaperRequest(form).logical_account_id).toBeUndefined();
  });

  it("加入已有组合时带组合编号、不带组合名称，由后端在同一个事务里加入；只开现货不设保证金模式", () => {
    const form = { ...createDefaultForm(), name: "a", markets: ["spot"] as 1[], join: "exist" as const, existingLogicalId: "l" };
    const request = buildPaperRequest(form);
    expect(request.logical_account_name).toBe("");
    expect(request.logical_account_id).toBe("l");
    expect(request.margin_mode).toBe("");
    expect(request.order_retention_days).toBe(5);
    expect(buildPaperRequest({ ...form, orderRetentionDays: " 0 " }).order_retention_days).toBe(0);
  });

  it("实盘请求带环境和密钥", () => {
    const form = { ...createDefaultForm("prod"), name: "k", secretId: "sec-1", markets: ["swap"] as 2[] };
    expect(buildLiveRequest(form)).toMatchObject({
      markets: ["swap"],
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
    expect(validateStep({ ...form, orderRetentionDays: "366" }, 2)).toContain("订单保留天数");
    expect(validateStep({ ...form, orderRetentionDays: "0" }, 2)).toBe("");
    expect(validateStep({ ...form, settlementAsset: "BUSD" }, 2)).toContain("USDT");
    expect(validateStep({ ...form, kind: "prod", ack: false }, 2)).toContain("提现权限");
    expect(validateStep({ ...form, join: "exist" }, 3)).toContain("选择");
    expect(validateStep({ ...form, join: "new" }, 3)).toBe("");
  });

  it("市场勾选保持固定顺序；只有现货时用现货费率", () => {
    expect(toggleMarket(["swap"], "spot", true)).toEqual(["spot", "swap"]);
    expect(toggleMarket(["spot", "swap"], "swap", false)).toEqual(["spot"]);
    expect(defaultFees(["spot"])).toEqual({ maker: "0.001", taker: "0.001" });
    expect(defaultFees(["spot", "swap"]).taker).toBe("0.0005");
  });

  it("加入组合只在会话还没建起来（TRADE_SESSION_NOT_READY）时重试，其他错误直接抛出", async () => {
    const sleep = () => Promise.resolve();
    const notReady = (code: number | string) => tradeFailure({ ret_info: { code, msg: "交易账户会话还没有启动，请稍后重试" } });
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

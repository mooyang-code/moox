import type { CreatePaperSimulationReq, CreateTradingAccountReq, Exchange, MarketType } from "@/api/trade/types";
import type { Environment } from "../shared/environment";

/** 1A 只开放模拟盘；测试网和生产环境要等交易所检测（T5）上线后再放开。 */
export const LIVE_ENVIRONMENTS_OPEN = false;

export const createKinds: readonly { key: Environment; label: string; desc: string; open: boolean }[] = [
  { key: "paper", label: "模拟盘", desc: "用真实行情在本地撮合，不连交易所，不需要密钥。", open: true },
  {
    key: "testnet",
    label: "测试网",
    desc: LIVE_ENVIRONMENTS_OPEN ? "连交易所测试环境，用测试币验证下单流程。" : "即将开放。",
    open: LIVE_ENVIRONMENTS_OPEN
  },
  {
    key: "prod",
    label: "生产环境",
    desc: LIVE_ENVIRONMENTS_OPEN ? "连交易所生产环境，用真实资金交易。" : "即将开放。",
    open: LIVE_ENVIRONMENTS_OPEN
  }
];

export interface CreateForm {
  step: 0 | 1 | 2 | 3;
  kind: Environment;
  exchange: Exchange;
  name: string;
  secretId: string;
  /** 生产环境：确认密钥没有提现权限 */
  ack: boolean;
  markets: MarketType[];
  settlementAsset: string;
  initialBalance: string;
  makerFeeRate: string;
  takerFeeRate: string;
  slippageBps: string;
  /** 费率被手工改过后，换市场不再覆盖 */
  feesEdited: boolean;
  join: "new" | "exist";
  logicalName: string;
  controlMode: 1 | 2;
  existingLogicalId: string;
}

export function defaultFees(markets: readonly MarketType[]): { maker: string; taker: string } {
  const spotOnly = markets.length > 0 && markets.every(market => market === 1);
  return spotOnly ? { maker: "0.001", taker: "0.001" } : { maker: "0.0002", taker: "0.0005" };
}

export function createDefaultForm(kind: Environment = "paper"): CreateForm {
  const markets: MarketType[] = [2];
  const fees = defaultFees(markets);
  return {
    step: 0,
    kind,
    exchange: 1,
    name: "",
    secretId: "",
    ack: false,
    markets,
    settlementAsset: "USDT",
    initialBalance: "100000",
    makerFeeRate: fees.maker,
    takerFeeRate: fees.taker,
    slippageBps: "0",
    feesEdited: false,
    join: "new",
    logicalName: "",
    controlMode: 1,
    existingLogicalId: ""
  };
}

/** 市场勾选结果按现货、永续的固定顺序排列。 */
export function toggleMarket(markets: readonly MarketType[], market: MarketType, on: boolean): MarketType[] {
  const next = new Set(markets);
  if (on) next.add(market);
  else next.delete(market);
  return ([1, 2] as MarketType[]).filter(item => next.has(item));
}

const nonNegativeDecimal = /^(?:0|[1-9]\d*)(?:\.\d+)?$/;

export function stepTitles(kind: Environment): string[] {
  return ["类型", kind === "paper" ? "交易所" : "交易所与密钥", "市场与参数", "组合"];
}

/** 校验当前步骤，返回第一条错误；通过时返回空串。 */
export function validateStep(form: CreateForm, step: number): string {
  if (step === 0) {
    return createKinds.find(item => item.key === form.kind)?.open ? "" : "这个环境还没有开放";
  }
  if (step === 1) {
    if (!form.name.trim()) return "请输入账户名称";
    if (form.kind !== "paper" && !form.secretId) return "请选择交易所密钥";
    return "";
  }
  if (step === 2) {
    if (!form.markets.length) return "至少开通一个市场";
    if (!form.settlementAsset.trim()) return "请输入结算资产";
    if (form.markets.includes(2) && form.settlementAsset.trim().toUpperCase() !== "USDT") return "永续只支持 USDT 结算";
    if (form.kind === "paper") {
      if (!nonNegativeDecimal.test(form.initialBalance.trim()) || Number(form.initialBalance) <= 0) {
        return "初始资金必须大于 0";
      }
      for (const value of [form.makerFeeRate, form.takerFeeRate, form.slippageBps]) {
        if (!nonNegativeDecimal.test(value.trim())) return "费率和滑点必须是非负数字";
      }
      if (Number(form.slippageBps) >= 10000) return "滑点必须小于 10000 基点";
    }
    if (form.kind === "prod" && !form.ack) return "请确认密钥没有提现权限";
    return "";
  }
  if (form.join === "exist" && !form.existingLogicalId) return "请选择要加入的组合";
  return "";
}

export function buildPaperRequest(form: CreateForm): CreatePaperSimulationReq {
  const swap = form.markets.includes(2);
  return {
    account_name: form.name.trim(),
    // 加入已有组合时不让后端新建组合：只建执行账户，再把它加进去
    logical_account_name: form.join === "new" ? form.logicalName.trim() || form.name.trim() : "",
    control_mode: form.controlMode,
    exchange: form.exchange,
    markets: [...form.markets],
    settlement_asset: form.settlementAsset.trim().toUpperCase(),
    margin_mode: swap ? "CROSS" : "",
    initial_balance: form.initialBalance.trim(),
    maker_fee_rate: form.makerFeeRate.trim(),
    taker_fee_rate: form.takerFeeRate.trim(),
    slippage_bps: form.slippageBps.trim()
  };
}

export function buildLiveRequest(form: CreateForm): CreateTradingAccountReq {
  return {
    name: form.name.trim(),
    exchange: form.exchange,
    markets: [...form.markets],
    settlement_asset: form.settlementAsset.trim().toUpperCase(),
    margin_mode: form.markets.includes(2) ? "CROSS" : "",
    live: { environment: form.kind === "testnet" ? 1 : 2, credential_secret_id: form.secretId }
  };
}

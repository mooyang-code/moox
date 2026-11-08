import type { CreatePaperSimulationReq, CreateTradingAccountReq, Exchange, MarketType } from "@/api/trade/types";
import { isTradeSessionNotReady } from "@/api/trade/http";
import type { Environment } from "../shared/environment";

export const createKinds: readonly { key: Environment; label: string; desc: string; open: boolean }[] = [
  { key: "paper", label: "模拟盘", desc: "用真实行情在本地撮合，不连交易所，不需要密钥。", open: true },
  { key: "testnet", label: "测试网", desc: "连交易所测试环境，用测试币验证下单流程。", open: true },
  { key: "prod", label: "生产环境", desc: "连交易所生产环境，用真实资金交易，选密钥后会先检测账户类型和权限。", open: true }
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
  /** 实盘：加入组合时接管账户里已有的持仓和余额（例如抵扣手续费用的 BNB），不勾时账户里有资产就加不进去 */
  adoptExisting: boolean;
}

/** A 股场所（ASHARE）：只开股票、按人民币结算；模拟盘只有一个佣金比例，最低 5 元、印花税、过户费由后端按常量计算。 */
export const ASHARE: Exchange = 3;
export const STOCK: MarketType = 3;
export const ASHARE_DEFAULT_BALANCE = "50000000";
export const ASHARE_DEFAULT_COMMISSION = "0.00025";

export function isAShare(form: Pick<CreateForm, "exchange">): boolean {
  return form.exchange === ASHARE;
}

export function defaultFees(markets: readonly MarketType[]): { maker: string; taker: string } {
  if (markets.includes(STOCK)) return { maker: ASHARE_DEFAULT_COMMISSION, taker: ASHARE_DEFAULT_COMMISSION };
  const spotOnly = markets.length > 0 && markets.every(market => market === 1);
  return spotOnly ? { maker: "0.001", taker: "0.001" } : { maker: "0.0002", taker: "0.0005" };
}

/**
 * 切换场所后重设和场所绑定的参数：A 股市场固定为股票、人民币结算、默认 5000 万元和佣金 0.025%；
 * 换回加密货币时恢复默认的永续、USDT 和费率。
 */
export function applyExchangeDefaults(form: CreateForm): void {
  const defaults = createDefaultForm(form.kind);
  if (isAShare(form)) {
    form.markets = [STOCK];
    form.settlementAsset = "CNY";
    form.initialBalance = ASHARE_DEFAULT_BALANCE;
  } else if (form.markets.includes(STOCK)) {
    form.markets = defaults.markets;
    form.settlementAsset = defaults.settlementAsset;
    form.initialBalance = defaults.initialBalance;
  }
  const fees = defaultFees(form.markets);
  form.makerFeeRate = fees.maker;
  form.takerFeeRate = fees.taker;
  form.feesEdited = false;
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
    existingLogicalId: "",
    adoptExisting: false
  };
}

/**
 * 按检测出的可开通市场收窄勾选：实盘账户只能开通检测结果里有的市场。
 * available 是检测结果的 SPOT/SWAP 字符串，为空表示没有限制（测试网跳过检测）；收窄后一个都不剩时取第一个可开通的市场。
 */
export function restrictMarkets(markets: readonly MarketType[], available: readonly string[]): MarketType[] {
  if (!available.length) return [...markets];
  const allowed = ([1, 2] as MarketType[]).filter(item => available.includes(item === 1 ? "SPOT" : "SWAP"));
  const kept = markets.filter(item => allowed.includes(item));
  return kept.length || !allowed.length ? kept : [allowed[0]];
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
    if (isAShare(form) && form.kind !== "paper") return "A 股实盘要通过 QMT 执行端下单，还没有接入";
    if (!form.name.trim()) return "请输入账户名称";
    if (form.kind !== "paper" && !form.secretId) return "请选择交易所密钥";
    return "";
  }
  if (step === 2) {
    if (isAShare(form)) {
      if (form.markets.length !== 1 || form.markets[0] !== STOCK) return "A 股只能开通股票市场";
      if (form.settlementAsset.trim().toUpperCase() !== "CNY") return "A 股按人民币（CNY）结算";
    } else if (form.markets.includes(STOCK)) {
      return "股票市场只能在 A 股场所开通";
    }
    if (!form.markets.length) return "至少开通一个市场";
    if (form.kind === "testnet" && form.exchange === 1 && form.markets.length > 1) return "币安测试网一个账户只能开通一个市场";
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
    // 新建组合：后端同时建组合；加入已有组合：后端在同一个事务里把新账户加进去，两者只给一个
    logical_account_name: form.join === "new" ? form.logicalName.trim() || form.name.trim() : "",
    logical_account_id: form.join === "exist" ? form.existingLogicalId : undefined,
    control_mode: form.controlMode,
    exchange: form.exchange,
    markets: [...form.markets],
    settlement_asset: form.settlementAsset.trim().toUpperCase(),
    margin_mode: swap ? "CROSS" : "",
    initial_balance: form.initialBalance.trim(),
    // A 股只有一个佣金比例：挂单、吃单相同
    maker_fee_rate: isAShare(form) ? form.takerFeeRate.trim() : form.makerFeeRate.trim(),
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

/**
 * 新建的实盘账户，运行时的会话要等周期检查才建起来：加入组合在这段时间里会返回 TRADE_SESSION_NOT_READY，
 * 只对这一种错误等几秒重试，其余错误原样抛出。
 */
export async function retryWhileSessionStarting<T>(
  call: () => Promise<T>,
  options: { attempts?: number; delayMs?: number; sleep?: (ms: number) => Promise<void> } = {}
): Promise<T> {
  const { attempts = 6, delayMs = 2000, sleep = ms => new Promise<void>(resolve => setTimeout(resolve, ms)) } = options;
  for (let attempt = 1; ; attempt++) {
    try {
      return await call();
    } catch (error) {
      if (!isTradeSessionNotReady(error) || attempt >= attempts) throw error;
      await sleep(delayMs);
    }
  }
}

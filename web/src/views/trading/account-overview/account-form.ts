import type { CreatePaperSimulationReq, CreateTradingAccountReq, MarketType } from "@/api/trade/types";

export interface AccountFormModel {
  name: string;
  exchange: 1 | 2;
  market_type: MarketType;
  execution_mode: 1 | 2;
  control_mode: 1 | 2;
  environment: 1 | 2;
  credential_secret_id: string;
  settlement_asset: string;
  margin_mode: string;
  initial_balance: string;
  maker_fee_rate: string;
  taker_fee_rate: string;
  slippage_bps: string;
  logical_account_name: string;
  sync_symbols: string;
}

export function normalizeSymbols(raw: string): string[] {
  return [
    ...new Set(
      raw
        .split(/[\s,]+/)
        .map(value => value.trim().toUpperCase())
        .filter(Boolean)
    )
  ];
}

export function createDefaultAccountForm(): AccountFormModel {
  return {
    name: "",
    exchange: 1,
    market_type: 1,
    execution_mode: 1,
    control_mode: 1,
    environment: 1,
    credential_secret_id: "",
    settlement_asset: "USDT",
    margin_mode: "CROSS",
    initial_balance: "100000",
    maker_fee_rate: "0",
    taker_fee_rate: "0",
    slippage_bps: "0",
    logical_account_name: "",
    sync_symbols: ""
  };
}

function isNonNegativeDecimal(value: string): boolean {
  return /^(?:0|[1-9]\d*)(?:\.\d+)?$/.test(value.trim());
}

export function validateAccountForm(form: AccountFormModel): string {
  if (!form.name.trim()) return "请输入账户名称";
  if (!form.settlement_asset.trim()) return "请输入结算资产";
  if (form.execution_mode === 2 && !form.credential_secret_id.trim()) return "请输入真实账户密钥标识";
  if (form.execution_mode === 1 && (!isNonNegativeDecimal(form.initial_balance) || Number(form.initial_balance) <= 0)) {
    return "模拟账户初始资金必须大于 0";
  }
  if (form.execution_mode === 2 && form.market_type === 1 && normalizeSymbols(form.sync_symbols).length === 0) {
    return "请至少填写一个交易标的";
  }
  if (form.execution_mode === 1) {
    for (const value of [form.maker_fee_rate, form.taker_fee_rate, form.slippage_bps]) {
      if (!isNonNegativeDecimal(value)) return "费率和滑点必须是非负数字";
    }
    if (Number(form.slippage_bps) >= 10000) return "滑点必须小于 10000 bps";
  }
  if (form.market_type === 2 && (form.settlement_asset.trim().toUpperCase() !== "USDT" || form.margin_mode !== "CROSS")) {
    return "合约账户仅支持 USDT/全仓";
  }
  return "";
}

export function buildLiveRequest(form: AccountFormModel): CreateTradingAccountReq {
  return {
    name: form.name.trim(),
    exchange: form.exchange,
    market_type: form.market_type,
    settlement_asset: form.settlement_asset.trim().toUpperCase(),
    margin_mode: form.market_type === 2 ? form.margin_mode : "",
    sync_symbols: normalizeSymbols(form.sync_symbols),
    live: { environment: form.environment, credential_secret_id: form.credential_secret_id.trim() }
  };
}

export function buildPaperSimulationRequest(form: AccountFormModel): CreatePaperSimulationReq {
  return {
    account_name: form.name.trim(),
    logical_account_name: (form.logical_account_name || `${form.name.trim()}-logical`).trim(),
    control_mode: form.control_mode,
    exchange: form.exchange,
    market_type: form.market_type,
    settlement_asset: form.settlement_asset.trim().toUpperCase(),
    margin_mode: form.market_type === 2 ? form.margin_mode : "",
    initial_balance: form.initial_balance.trim(),
    maker_fee_rate: form.maker_fee_rate.trim(),
    taker_fee_rate: form.taker_fee_rate.trim(),
    slippage_bps: form.slippage_bps.trim()
  };
}

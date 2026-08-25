import type { CreatePaperSimulationReq, CreateTradingAccountReq, MarketType } from "@/api/trade/types";

export interface AccountFormModel {
  name: string;
  exchange: 1 | 2;
  market_type: MarketType;
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

function symbols(raw: string): string[] {
  return raw.split(",").map(value => value.trim().toUpperCase()).filter(Boolean);
}

export function buildLiveRequest(form: AccountFormModel): CreateTradingAccountReq {
  return {
    name: form.name.trim(),
    exchange: form.exchange,
    market_type: form.market_type,
    settlement_asset: form.settlement_asset.trim().toUpperCase(),
    margin_mode: form.market_type === 2 ? form.margin_mode : "",
    sync_symbols: symbols(form.sync_symbols),
    live: { environment: form.environment, credential_secret_id: form.credential_secret_id.trim() }
  };
}

export function buildPaperSimulationRequest(form: AccountFormModel): CreatePaperSimulationReq {
  return {
    account_name: form.name.trim(),
    logical_account_name: (form.logical_account_name || `${form.name.trim()}-logical`).trim(),
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

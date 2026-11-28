import type { Exchange, InspectExchangeCredentialReq, MarketType } from "@/api/trade/types";
import { sortMarkets } from "@/views/trading/accounts/shared/markets";

// 密钥管理里交易所密钥的“提供方”（binance、okx）就是 Trade 的交易所写法；其他提供方不能检测，返回 undefined。
export function exchangeFromProvider(provider: string | undefined): Exchange | undefined {
  const value = (provider ?? "").trim().toLowerCase();
  return value === "binance" || value === "okx" ? value : undefined;
}

export interface InspectionRequestInput {
  provider: string | undefined;
  secretId: string;
  environment: 1 | 2;
  markets: MarketType[];
}

export function inspectionRequest(input: InspectionRequestInput): InspectExchangeCredentialReq {
  const exchange = exchangeFromProvider(input.provider);
  if (!exchange) throw new Error("只有币安和 OKX 的交易所密钥可以检测");
  const request: InspectExchangeCredentialReq = {
    exchange,
    environment: input.environment,
    credential_secret_id: input.secretId
  };
  // 不勾选市场表示预览：不传 markets，后端只给和市场无关的阻断项，和市场有关的问题写成提醒。
  if (input.markets.length > 0) request.markets = sortMarkets(input.markets);
  return request;
}

// 密钥是否可以做测试连接：交易所分类、币安或 OKX、启用状态。
export function canTestConnection(secret: { category?: string; provider?: string; status?: string }): boolean {
  return secret.category === "exchange" && exchangeFromProvider(secret.provider) !== undefined && secret.status === "active";
}

/**
 * 从 extra_config 的 environment 字段读取交易所密钥所属环境：
 * "testnet" → 1（测试网），"production" → 2（生产环境）；缺省或解析不了时返回 0。
 */
export function secretEnvironmentOf(extraConfig: string | undefined): 0 | 1 | 2 {
  if (!extraConfig) return 0;
  try {
    const parsed = JSON.parse(extraConfig) as Record<string, unknown>;
    const env = String(parsed?.environment ?? "")
      .trim()
      .toLowerCase();
    if (env === "testnet") return 1;
    if (env === "production") return 2;
    return 0;
  } catch {
    return 0;
  }
}

/** 只读密钥（extra_config 里 "readonly":"true"）：只用于读行情与下架检测这类只读接口，不能建执行账户。 */
export function isReadonlySecret(extraConfig: string | undefined): boolean {
  if (!extraConfig) return false;
  try {
    const parsed = JSON.parse(extraConfig) as Record<string, unknown>;
    return String(parsed?.readonly ?? "").toLowerCase() === "true";
  } catch {
    return false;
  }
}

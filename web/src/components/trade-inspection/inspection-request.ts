import type { Exchange, InspectExchangeCredentialReq, MarketType } from "@/api/trade/types";

// 密钥管理里交易所密钥的“提供方”（binance、okx）对应 Trade 的交易所枚举；其他提供方不能检测。
export function exchangeFromProvider(provider: string | undefined): Exchange {
  switch ((provider ?? "").trim().toLowerCase()) {
    case "binance":
      return 1;
    case "okx":
      return 2;
    default:
      return 0;
  }
}

export interface InspectionRequestInput {
  provider: string | undefined;
  secretId: string;
  environment: 1 | 2;
  markets: MarketType[];
}

export function inspectionRequest(input: InspectionRequestInput): InspectExchangeCredentialReq {
  const exchange = exchangeFromProvider(input.provider);
  if (exchange === 0) throw new Error("只有币安和 OKX 的交易所密钥可以检测");
  const request: InspectExchangeCredentialReq = {
    exchange,
    environment: input.environment,
    credential_secret_id: input.secretId
  };
  // 不勾选市场表示预览：不传 markets，后端只给和市场无关的阻断项，和市场有关的问题写成提醒。
  if (input.markets.length > 0) request.markets = [...input.markets].sort((a, b) => a - b);
  return request;
}

// 密钥是否可以做测试连接：交易所分类、币安或 OKX、启用状态。
export function canTestConnection(secret: { category?: string; provider?: string; status?: string }): boolean {
  return secret.category === "exchange" && exchangeFromProvider(secret.provider) !== 0 && secret.status === "active";
}

/**
 * 从密钥 ID 里读环境：setup apply 写入的交易所密钥固定为 exchange-<交易所>-<环境>-<名称>，
 * 环境段是 production 或 testnet，对应 Trade 的 2、1。不符合这个格式（例如管理台手工录入的）时返回 0，表示看不出来。
 */
export function secretEnvironmentOf(secretId: string | undefined): 0 | 1 | 2 {
  const match = /^exchange-[a-z0-9]+-(production|testnet)-/.exec((secretId ?? "").trim().toLowerCase());
  if (!match) return 0;
  return match[1] === "testnet" ? 1 : 2;
}

/** 只读密钥（extra_config 里 "readonly":"true"）：只用于读行情这类接口，不能建执行账户。 */
export function isReadonlySecret(extraConfig: string | undefined): boolean {
  if (!extraConfig) return false;
  try {
    const parsed = JSON.parse(extraConfig) as Record<string, unknown>;
    return String(parsed?.readonly ?? "").toLowerCase() === "true";
  } catch {
    return false;
  }
}

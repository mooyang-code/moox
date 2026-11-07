import type { Exchange, InspectExchangeCredentialReq, MarketType } from "@/api/trade/types";

// 秘钥管理里交易所密钥的“提供方”（binance、okx）对应 Trade 的交易所枚举；其他提供方不能检测。
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
  // 不勾选市场表示预览：不传 markets，由后端按这把密钥能开通的全部市场判断。
  if (input.markets.length > 0) request.markets = [...input.markets].sort((a, b) => a - b);
  return request;
}

// 秘钥是否可以做测试连接：交易所分类、币安或 OKX、启用状态。
export function canTestConnection(secret: { category?: string; provider?: string; status?: string }): boolean {
  return secret.category === "exchange" && exchangeFromProvider(secret.provider) !== 0 && secret.status === "active";
}

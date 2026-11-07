import { listSecrets } from "@/api/admin/secret";
import type { Exchange } from "@/api/trade/types";

export interface ExchangeSecretOption {
  secretId: string;
  name: string;
}

const providers: Record<number, string> = { 1: "binance", 2: "okx" };

/** 秘钥管理里启用的交易所密钥，按交易所筛选；这里只选名称，不接触密钥原文。 */
export async function loadExchangeSecrets(exchange: Exchange): Promise<ExchangeSecretOption[]> {
  const rsp = await listSecrets({ category: "exchange", status: "active", offset: 0, limit: 200 });
  const provider = providers[exchange];
  return (rsp.secrets || [])
    .filter(item => item.secret_id && (!item.provider || item.provider.toLowerCase() === provider))
    .map(item => ({ secretId: item.secret_id as string, name: item.name }));
}

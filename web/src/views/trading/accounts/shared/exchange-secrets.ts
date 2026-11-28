import { listSecrets } from "@/api/admin/secret";
import { isReadonlySecret, secretEnvironmentOf } from "@/components/trade-inspection/inspection-request";
import type { Exchange } from "@/api/trade/types";

export interface ExchangeSecretOption {
  secretId: string;
  name: string;
}

/**
 * 密钥管理里启用的交易所密钥，按交易所筛选；这里只选名称，不接触密钥原文。
 * 只读密钥不能建执行账户，直接排除；给了环境（1 测试网、2 生产环境）时，再排除 extra_config.environment 明确是另一个环境的
 * （未标环境的照常列出，由检测判断）。
 */
export async function loadExchangeSecrets(exchange: Exchange, environment?: 1 | 2): Promise<ExchangeSecretOption[]> {
  const rsp = await listSecrets({ category: "exchange", status: "active", page: { page: 1, size: 200 } });
  // 密钥的提供方就是 Trade 的交易所写法（binance、okx）
  const provider = exchange;
  return (rsp.secrets || [])
    .filter(item => item.secret_id && (!item.provider || item.provider.toLowerCase() === provider))
    .filter(item => !isReadonlySecret(item.extra_config))
    .filter(item => {
      if (!environment) return true;
      const env = secretEnvironmentOf(item.extra_config);
      return env === 0 || env === environment;
    })
    .map(item => ({ secretId: item.secret_id as string, name: item.name }));
}

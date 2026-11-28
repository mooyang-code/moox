export type ExchangeEnvironment = "production" | "testnet";

export interface ExchangeExtraState {
  environment: ExchangeEnvironment;
  readonly: boolean;
  passphrase: string;
  hasPassphrase: boolean;
  other: Record<string, string>;
}

const MASKED_EXTRA_VALUE = "******";

/**
 * 解析交易所密钥的 extra_config（列表接口返回的敏感项 passphrase 已被后端脱敏为 "******"）。
 */
export function parseExchangeExtra(extraConfig: string | undefined): ExchangeExtraState {
  const state: ExchangeExtraState = {
    environment: "production",
    readonly: false,
    passphrase: "",
    hasPassphrase: false,
    other: {}
  };
  if (!extraConfig) return state;
  try {
    const parsed = JSON.parse(extraConfig) as Record<string, unknown>;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return state;
    for (const [key, raw] of Object.entries(parsed)) {
      if (raw == null) continue;
      const value = String(raw);
      if (key === "environment") {
        state.environment = value.trim().toLowerCase() === "testnet" ? "testnet" : "production";
      } else if (key === "readonly") {
        state.readonly = value.trim().toLowerCase() === "true";
      } else if (key === "passphrase") {
        state.hasPassphrase = value.length > 0;
      } else {
        state.other[key] = value;
      }
    }
  } catch {
    // 非法 JSON 时退回默认值
  }
  return state;
}

/**
 * 序列化交易所密钥的 extra_config。
 * 后端 Trade 将 extra_config 解码为 map[string]string，这里所有值一律写成字符串。
 * 编辑 OKX 密钥时 passphrase 留空且原记录已有 passphrase，回传 "******" 让服务端保留原值。
 */
export function buildExchangeExtra(input: {
  provider: string;
  environment: ExchangeEnvironment;
  readonly: boolean;
  passphrase: string;
  hasPassphrase: boolean;
  other?: Record<string, string>;
}): string {
  const out: Record<string, string> = { ...(input.other ?? {}) };
  out.environment = input.environment === "testnet" ? "testnet" : "production";
  if (input.readonly) {
    out.readonly = "true";
  } else {
    delete out.readonly;
  }
  if (input.provider.trim().toLowerCase() === "okx") {
    const trimmed = input.passphrase.trim();
    if (trimmed) {
      out.passphrase = trimmed;
    } else if (input.hasPassphrase) {
      out.passphrase = MASKED_EXTRA_VALUE;
    } else {
      delete out.passphrase;
    }
  } else {
    delete out.passphrase;
  }
  return JSON.stringify(out);
}

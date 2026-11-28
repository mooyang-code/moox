export type ExchangeEnvironment = "production" | "testnet";
export type ExchangeEnvironmentValue = ExchangeEnvironment | "";

export interface ExchangeExtraState {
  /** 空串表示旧密钥未标注环境，编辑保存前需由用户明确选择。 */
  environment: ExchangeEnvironmentValue;
  readonly: boolean;
  passphrase: string;
  hasPassphrase: boolean;
  other: Record<string, string>;
}

const MASKED_EXTRA_VALUE = "******";

/** 列表展示交易所密钥的环境标签：缺省环境显式标注“未标注（按生产处理）”。 */
export function exchangeEnvironmentDisplay(extraConfig: string | undefined): { label: string; color: string } {
  const env = parseExchangeExtra(extraConfig).environment;
  if (env === "testnet") return { label: "测试网", color: "arcoblue" };
  if (env === "production") return { label: "生产", color: "green" };
  return { label: "未标注（按生产处理）", color: "orange" };
}

/**
 * 解析交易所密钥的 extra_config（列表接口返回的敏感项 passphrase 已被后端脱敏为 "******"）。
 * 未写明 environment 或值无法识别时保持空串，不静默填成 production。
 */
export function parseExchangeExtra(extraConfig: string | undefined): ExchangeExtraState {
  const state: ExchangeExtraState = {
    environment: "",
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
        const normalized = value.trim().toLowerCase();
        if (normalized === "testnet") {
          state.environment = "testnet";
        } else if (normalized === "production") {
          state.environment = "production";
        }
      } else if (key === "readonly") {
        state.readonly = value.trim().toLowerCase() === "true";
      } else if (key === "passphrase") {
        state.hasPassphrase = value.length > 0;
      } else {
        state.other[key] = value;
      }
    }
  } catch {
    // 非法 JSON 时保持未标注环境
  }
  return state;
}

/**
 * 序列化交易所密钥的 extra_config。
 * 后端 Trade 将 extra_config 解码为 map[string]string，这里所有值一律写成字符串；
 * environment 必须明确为 production 或 testnet，拒绝静默回退。
 * 编辑 OKX 密钥时 passphrase 留空且原记录已有 passphrase，回传 "******" 让服务端保留原值。
 */
export function buildExchangeExtra(input: {
  provider: string;
  environment: ExchangeEnvironmentValue;
  readonly: boolean;
  passphrase: string;
  hasPassphrase: boolean;
  other?: Record<string, string>;
}): string {
  if (input.environment !== "production" && input.environment !== "testnet") {
    throw new Error("请明确选择交易所密钥所属环境（生产环境或测试网）");
  }
  const out: Record<string, string> = { ...(input.other ?? {}) };
  out.environment = input.environment;
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

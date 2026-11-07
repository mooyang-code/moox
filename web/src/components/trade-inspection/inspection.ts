import type { ExchangeInspection, InspectionFinding } from "@/api/trade/types";

// 检测结果的展示模型：RPC 返回（ExchangeInspection）和账户里存的 inspection_json 形状不同，都转成这一份给组件显示。
export interface InspectionView {
  accountMode: string;
  skipped: boolean;
  canWithdraw: boolean;
  canTradeSpot: boolean;
  canTradeSwap: boolean;
  universalTransfer: boolean;
  ipRestricted: boolean;
  rawPermissions: string;
  okxAcctLv: string;
  positionMode: string;
  availableMarkets: string[];
  blockers: InspectionFinding[];
  warnings: InspectionFinding[];
  inspectedAt: number;
  consecutiveFailures: number;
  lastFailure: string;
}

const MARKET_BY_ENUM: Record<number, string> = { 1: "SPOT", 2: "SWAP", 3: "STOCK" };

export const MARKET_LABELS: Record<string, string> = { SPOT: "现货", SWAP: "永续合约", STOCK: "股票" };

const ACCOUNT_MODE_LABELS: Record<string, string> = {
  BINANCE_PORTFOLIO_MARGIN: "币安统一账户（组合保证金）",
  BINANCE_STANDARD: "币安普通账户（现货、合约两个钱包）",
  OKX_UNIFIED: "OKX 统一账户"
};

const POSITION_MODE_LABELS: Record<string, string> = { ONE_WAY: "单向持仓", HEDGE: "双向持仓" };

const OKX_ACCT_LV_LABELS: Record<string, string> = {
  "1": "现货模式",
  "2": "合约模式",
  "3": "跨币种保证金",
  "4": "组合保证金"
};

export function accountModeLabel(mode: string | undefined): string {
  if (!mode) return "未检测";
  return ACCOUNT_MODE_LABELS[mode] ?? mode;
}

export function marketLabel(market: string): string {
  return MARKET_LABELS[market] ?? market;
}

function toMillis(value: unknown): number {
  if (typeof value === "number") return value;
  if (typeof value === "string" && value !== "") {
    const numeric = Number(value);
    if (Number.isFinite(numeric) && /^\d+$/.test(value)) return numeric;
    const parsed = Date.parse(value);
    if (!Number.isNaN(parsed)) return parsed;
  }
  return 0;
}

function findings(value: unknown): InspectionFinding[] {
  if (!Array.isArray(value)) return [];
  return value
    .filter((item): item is InspectionFinding => !!item && typeof item === "object" && "message" in item)
    .map(item => ({ code: String(item.code ?? ""), message: String(item.message) }));
}

// 来自 RPC 的检测结果。
export function fromInspection(value: ExchangeInspection | undefined | null): InspectionView {
  const raw = value ?? {};
  return {
    accountMode: raw.account_mode ?? "",
    skipped: !!raw.skipped,
    canWithdraw: !!raw.can_withdraw,
    canTradeSpot: !!raw.can_trade_spot,
    canTradeSwap: !!raw.can_trade_swap,
    universalTransfer: !!raw.universal_transfer,
    ipRestricted: !!raw.ip_restricted,
    rawPermissions: raw.raw_permissions ?? "",
    okxAcctLv: raw.okx_acct_lv ?? "",
    positionMode: raw.position_mode ?? "",
    availableMarkets: (raw.available_markets ?? []).map(item => MARKET_BY_ENUM[item] ?? String(item)),
    blockers: findings(raw.blockers),
    warnings: findings(raw.warnings),
    inspectedAt: toMillis(raw.inspected_at),
    consecutiveFailures: raw.consecutive_failures ?? 0,
    lastFailure: raw.last_failure ?? ""
  };
}

// 来自账户里存的 inspection_json（后端按下划线字段名存储，市场是字符串、时间是 RFC3339）。空串和 "{}" 表示还没检测过，返回 null。
export function fromStoredInspection(json: string | undefined | null): InspectionView | null {
  if (!json || json.trim() === "" || json.trim() === "{}") return null;
  let raw: Record<string, unknown>;
  try {
    raw = JSON.parse(json) as Record<string, unknown>;
  } catch {
    return null;
  }
  const text = (key: string) => (typeof raw[key] === "string" ? (raw[key] as string) : "");
  const flag = (key: string) => raw[key] === true;
  const inspectedAt = toMillis(raw.inspected_at);
  return {
    accountMode: text("account_mode"),
    skipped: flag("skipped"),
    canWithdraw: flag("can_withdraw"),
    canTradeSpot: flag("can_trade_spot"),
    canTradeSwap: flag("can_trade_swap"),
    universalTransfer: flag("universal_transfer"),
    ipRestricted: flag("ip_restricted"),
    rawPermissions: text("raw_permissions"),
    okxAcctLv: text("okx_acct_lv"),
    positionMode: text("position_mode"),
    availableMarkets: Array.isArray(raw.available_markets) ? (raw.available_markets as unknown[]).map(String) : [],
    blockers: findings(raw.blockers),
    warnings: findings(raw.warnings),
    // Go 的零时间序列化成 0001-01-01，当作没有检测时间。
    inspectedAt: inspectedAt > 0 ? inspectedAt : 0,
    consecutiveFailures: typeof raw.consecutive_failures === "number" ? raw.consecutive_failures : 0,
    lastFailure: text("last_failure")
  };
}

export type InspectionVerdict = "blocked" | "ok" | "skipped";

export function inspectionVerdict(view: InspectionView): InspectionVerdict {
  if (view.blockers.length > 0) return "blocked";
  if (view.skipped) return "skipped";
  return "ok";
}

export interface InspectionRow {
  label: string;
  value: string;
}

// 检测结果的明细行：账户类型、可开通的市场、权限、持仓模式等，读不到的项不显示。
export function inspectionRows(view: InspectionView): InspectionRow[] {
  const rows: InspectionRow[] = [{ label: "账户类型", value: accountModeLabel(view.accountMode) }];
  if (view.skipped) {
    rows.push({ label: "检测范围", value: "测试网没有账户类型和权限接口，已跳过检测" });
    return rows;
  }
  rows.push({
    label: "可开通市场",
    value: view.availableMarkets.length ? view.availableMarkets.map(marketLabel).join("、") : "无"
  });
  rows.push({ label: "提现权限", value: view.canWithdraw ? "已开启（必须关闭）" : "未开启" });
  if (view.accountMode === "BINANCE_STANDARD") {
    rows.push({ label: "万向划转权限", value: view.universalTransfer ? "已开启" : "未开启" });
  }
  rows.push({ label: "IP 白名单", value: view.ipRestricted ? "已绑定" : "未绑定" });
  if (view.positionMode)
    rows.push({ label: "合约持仓模式", value: POSITION_MODE_LABELS[view.positionMode] ?? view.positionMode });
  if (view.okxAcctLv) rows.push({ label: "OKX 账户层级", value: OKX_ACCT_LV_LABELS[view.okxAcctLv] ?? view.okxAcctLv });
  return rows;
}

// 复查连续失败的说明：传输错误不下结论，保留上次结果。
export function inspectionFailureNote(view: InspectionView): string {
  if (view.consecutiveFailures <= 0) return "";
  const reason = view.lastFailure ? `：${view.lastFailure}` : "";
  return `最近 ${view.consecutiveFailures} 次复查没有检测成功，当前显示的是上次的结果${reason}`;
}

import type { ExecutionMode, TradingAccount } from "@/api/trade/types";

export type AccountModeTab = "all" | "live" | "paper";

export const accountModeTabs = [
  { key: "all", label: "全部" },
  { key: "live", label: "真实账户" },
  { key: "paper", label: "模拟账户" }
] as const;

export function accountModeFromQuery(value: unknown): AccountModeTab {
  return value === "live" || value === "paper" ? value : "all";
}

export function accountModeToExecutionMode(mode: AccountModeTab): ExecutionMode | undefined {
  if (mode === "live") return 2;
  if (mode === "paper") return 1;
  return undefined;
}

export function accountTypeLabel(account: Pick<TradingAccount, "execution_mode" | "paper" | "live">): string {
  if (account.execution_mode === 1 || account.paper) return "模拟账户";
  if (account.execution_mode === 2 || account.live) return "真实账户";
  return "未知账户";
}

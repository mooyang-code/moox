import { environmentLabels, executionModeLabels } from "@/api/trade";
import type { TradingAccount } from "@/api/trade/types";

export function accountStatusView(
  status: string,
  ready: boolean
): {
  label: string;
  color: "green" | "orange" | "red" | "gray";
} {
  if (status === "ERROR") return { label: "错误", color: "red" };
  if (status === "DISABLED" || status === "CLOSED") return { label: "已停用", color: "gray" };
  if (status !== "ENABLED") return { label: status || "未知", color: "gray" };
  if (!ready) return { label: "未就绪", color: "orange" };
  if (status === "ENABLED") return { label: "就绪", color: "green" };
  return { label: status || "未知", color: "gray" };
}

export function accountEnvironmentView(account: TradingAccount): string {
  if (account.execution_mode === 1 || account.paper) return "Paper";
  if (account.execution_mode !== 2 || !account.live) return executionModeLabels[account.execution_mode] || "-";
  return environmentLabels[account.live.environment] || "Unknown";
}

export function snapshotValue(value?: string): string {
  return value?.trim() || "-";
}

export function snapshotPnlClass(value?: string): "positive" | "negative" | "" {
  if (!value?.trim()) return "";
  const numeric = Number(value);
  if (!Number.isFinite(numeric) || numeric === 0) return "";
  return numeric > 0 ? "positive" : "negative";
}

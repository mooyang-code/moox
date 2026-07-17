import type { ServiceDeployment, ServiceDeploymentInput } from "@/api/admin/types";

export function serviceDeploymentRowKey(deployment: Pick<ServiceDeployment, "node_id" | "service_name">) {
  return `${deployment.node_id}:${deployment.service_name}`;
}

export function validateGatewayDeployment(
  deployment: Pick<ServiceDeploymentInput, "gateway_enabled" | "host" | "gateway_path" | "gateway_service_id">
) {
  if (!deployment.gateway_enabled) return "";
  if (deployment.host !== "127.0.0.1" && deployment.host !== "::1") return "网关暴露的服务 Host 只能是 127.0.0.1 或 ::1";
  if (!deployment.gateway_service_id.trim()) return "请填写 Gateway service ID";
  const servicePath = deployment.gateway_path?.trim() || "";
  if (!servicePath || !servicePath.startsWith("trpc.")) return "请填写有效的 tRPC service path";
  return "";
}

export function gatewayNodeOnlineState(lastSeenAt?: string, now = new Date()) {
  if (!lastSeenAt) return { state: "never" as const, label: "未上报" };
  const timestamp = new Date(lastSeenAt).getTime();
  if (!Number.isFinite(timestamp)) return { state: "never" as const, label: "未上报" };
  const online = now.getTime() - timestamp <= 2 * 60 * 1000;
  return online ? { state: "online" as const, label: "在线" } : { state: "offline" as const, label: "离线" };
}

export function gatewayHashState(expected?: string, applied?: string) {
  if (!expected || expected !== applied) return { state: "mismatch" as const, label: "待同步" };
  return { state: "synced" as const, label: "已同步" };
}

export function validateGatewayControlURL(value: string) {
  let parsed: URL;
  try {
    parsed = new URL(value.trim());
  } catch {
    return "请填写有效的 Gateway URL";
  }
  if (parsed.username || parsed.password || parsed.search || parsed.hash || (parsed.pathname && parsed.pathname !== "/")) {
    return "Gateway URL 只能包含协议、主机和端口";
  }
  const loopback =
    parsed.hostname === "127.0.0.1" ||
    parsed.hostname === "[::1]" ||
    parsed.hostname === "::1" ||
    parsed.hostname === "localhost";
  if (parsed.protocol !== "https:" && !(parsed.protocol === "http:" && loopback)) {
    return "Gateway URL 必须使用 HTTPS，仅本机开发地址可使用 HTTP";
  }
  return "";
}

export async function runModalSubmission(
  validate: () => string,
  submit: () => Promise<unknown>,
  onError?: (error: unknown) => void
) {
  if (validate()) return false;
  try {
    await submit();
    return true;
  } catch (error) {
    onError?.(error);
    return false;
  }
}

export function createLatestRequestGuard() {
  let generation = 0;
  return {
    begin: () => ++generation,
    isCurrent: (value: number) => value === generation,
    invalidate: () => {
      generation += 1;
    }
  };
}

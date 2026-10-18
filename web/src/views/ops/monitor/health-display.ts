import type { HealthObject } from "@/api/monitor";

export function statusLabel(status?: string) {
  switch (status) {
    case "healthy":
      return "正常";
    case "degraded":
      return "需关注";
    case "down":
      return "异常";
    case "disabled":
      return "已停用";
    case "unchecked":
      return "不探测";
    default:
      return "未知";
  }
}

export function statusColor(status?: string) {
  switch (status) {
    case "healthy":
      return "green";
    case "degraded":
      return "orange";
    case "down":
      return "red";
    default:
      return "gray";
  }
}

export function formatCheckedAt(value?: string) {
  if (!value) return "暂无";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "暂无" : date.toLocaleString("zh-CN", { hour12: false });
}

export function hostHref(agentID?: string, hostID?: string) {
  return (
    "#/ops/hosts?" +
    new URLSearchParams({ tab: "monitor", ...(agentID ? { agent_id: agentID } : {}), ...(hostID ? { host_id: hostID } : {}) })
  );
}

const COLLECTOR_RESULTS = { href: "#/collector/tasks?tab=results", label: "查看采集结果" };

/** 告警对象的处理入口：页面地址与链接文字。 */
export function alertLink(object?: HealthObject): { href: string; label: string } {
  if (object?.type === "host") return { href: hostHref(object.agent_id, object.host_id), label: "查看主机" };
  if (object?.type === "dataset") {
    switch (object.producer) {
      case "factor":
        return { href: "#/factor/tasks?tab=results", label: "查看因子结果" };
      case "strategy":
        return { href: "#/strategy/tasks", label: "查看策略实例" };
      case "trade":
        return { href: "#/trading/accounts", label: "查看交易账户" };
      default:
        return COLLECTOR_RESULTS;
    }
  }
  switch (object?.kind) {
    case "kline_freshness":
    case "market_canary":
    case "market_fetch":
      return COLLECTOR_RESULTS;
    case "data_delivery":
      return { href: "#/ops/storage/nodes", label: "查看存储节点" };
    case "balance":
      return { href: "#/trading/accounts", label: "查看交易账户" };
  }
  return {
    href:
      "#/ops/deployments?" +
      new URLSearchParams({
        tab: "services",
        ...(object?.host_id ? { host_id: object.host_id } : {}),
        ...(object?.component_id ? { component_id: object.component_id } : {})
      }),
    label: "查看服务"
  };
}

export function alertHref(item: { object?: HealthObject }) {
  return alertLink(item.object).href;
}

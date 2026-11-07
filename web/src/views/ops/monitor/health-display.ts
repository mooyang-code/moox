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

// 主机页只按登记的 host_id 定位；历史曲线用的采集器由页面按该主机最近上报的采集器选择。
export function hostHref(hostID?: string) {
  return "#/ops/hosts?" + new URLSearchParams({ tab: "monitor", ...(hostID ? { host_id: hostID } : {}) });
}

const COLLECTOR_RESULTS = { href: "#/collector/tasks?tab=results", label: "查看采集结果" };

/** 缺少币安只读 Key 的告警检查 ID：告警编号是“告警规则 ID:检查 ID”，按完整的检查 ID 结尾认出它。 */
const READONLY_KEY_MISSING_CHECK = "collector_attention:delist_readonly_key_missing";

/** 告警对象的处理入口：页面地址与链接文字。alertId 用来区分同一类关注项里的具体情况。 */
export function alertLink(object?: HealthObject, alertId?: string): { href: string; label: string } {
  if (object?.kind === "collector_attention" && alertId?.endsWith(`:${READONLY_KEY_MISSING_CHECK}`)) {
    return { href: "#/settings/secrets", label: "去录入币安只读 Key" };
  }
  if (object?.type === "host") return { href: hostHref(object.host_id), label: "查看主机" };
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
    case "trade_attention":
      return { href: "#/trading/accounts", label: "查看交易账户" };
    case "event_center_attention":
      return { href: "#/settings/events?tab=executions", label: "查看事件中心执行记录" };
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

export function alertHref(item: { object?: HealthObject; id?: string }) {
  return alertLink(item.object, item.id).href;
}

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

function readonlyKeyMissing(object?: HealthObject): boolean {
  return object?.kind === "collector_attention" && object.attention_kind === "delist_readonly_key_missing";
}

/**
 * 没有页面能处理、要在命令行里处理的告警：给出处理办法的说明文字，监控页用它代替跳转链接；其余返回空串。
 * 缺币安只读 Key：Collector 只读固定 ID exchange-binance-production-readonly，秘钥页新增的 ID 是随机的，补不上这把 Key。
 */
export function alertNote(object?: HealthObject): string {
  if (readonlyKeyMissing(object)) {
    return (
      "运行 moox-cli config edit，在“交易所密钥”里加一把币安生产环境的只读 Key（名称填 readonly，勾选只读，" +
      '即 [[exchange_keys]] 里 exchange = "binance"、environment = "production"、name = "readonly"、readonly = true），' +
      "再运行 moox-cli setup apply 写入。补上之前，现货下架只能用 moox-cli collector delist register 手工登记"
    );
  }
  return "";
}

/** 告警对象的处理入口：页面地址与链接文字。同一类关注项里的具体情况按 Monitor 给出的 attention_kind 区分。 */
export function alertLink(object?: HealthObject): { href: string; label: string } {
  // 只能在命令行里处理：首页跳到监控告警页，那里显示处理说明（alertNote）。
  if (readonlyKeyMissing(object)) return { href: "#/ops/monitor", label: "查看处理办法" };
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

export function alertHref(item: { object?: HealthObject }) {
  return alertLink(item.object).href;
}

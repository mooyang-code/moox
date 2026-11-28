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
      return "不检查";
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

// 主机页只按登记的 host_id 定位；历史曲线用的采集器由页面按该主机最近上报的采集器选择。
export function hostHref(hostID?: string) {
  return "#/ops/hosts?" + new URLSearchParams({ tab: "monitor", ...(hostID ? { host_id: hostID } : {}) });
}

const COLLECTOR_RESULTS = { href: "#/collector/tasks?tab=results", label: "查看采集结果" };

function readonlyKeyMissing(object?: HealthObject): boolean {
  return (
    object?.kind === "attention" && object.component_id === "collector" && object.attention_kind === "delist_readonly_key_missing"
  );
}

/** 告警的处理说明：几段文字，外加可选的处理入口。 */
export interface AlertNote {
  lines: string[];
  /** 说明里某一种情况能在页面上处理时给出入口；监控页用它代替默认的跳转链接。 */
  link?: { href: string; label: string };
}

/**
 * 需要先看说明的告警：返回处理说明；其余返回 null。
 * 缺币安只读 Key：Collector 按属性（类别 exchange、提供方 binance、状态启用、生产环境或未标环境、readonly=true）查找，
 * 找不到时直接引导用户到管理台密钥管理页启用或新建。
 */
export function alertNote(object?: HealthObject): AlertNote | null {
  if (!readonlyKeyMissing(object)) return null;
  return {
    lines: [
      "已有币安生产环境只读密钥但被禁用：到密钥管理页找到它并启用即可恢复。",
      "还没有这把密钥：在密钥管理页点击“新建密钥”，分类选“交易所”、提供方选 binance、环境选“生产环境”并勾选“只读密钥”，填写币安只读 API Key / Secret Key，保存后下一轮检测自动恢复。",
      "补上之前，现货下架只能手工登记：运行 moox-cli collector delist register，或在交易 → 下架名单页点“手工登记下架”。"
    ],
    link: { href: "#/settings/secrets", label: "前往密钥管理页配置" }
  };
}

/** 告警的处理入口。spaceId：对象所在的空间，和当前空间不同时先切过去再打开（数据集属于某个空间）。 */
export interface AlertLink {
  href: string;
  label: string;
  spaceId?: string;
}

/** 采集数据集的告警：直达这个数据集的采集结果（结果页按 ?dataset=&freq= 选任务）。 */
function collectorDatasetLink(object: HealthObject): AlertLink {
  if (!object.dataset_id) return COLLECTOR_RESULTS;
  const query = new URLSearchParams({
    tab: "results",
    dataset: object.dataset_id,
    ...(object.freq ? { freq: object.freq } : {})
  });
  return { href: `#/collector/tasks?${query}`, label: "查看采集结果", ...(object.space_id ? { spaceId: object.space_id } : {}) };
}

/** 告警对象的处理入口：页面地址与链接文字。关注项按 Monitor 给出的服务名（component_id）与种类（attention_kind）区分。 */
export function alertLink(object?: HealthObject): AlertLink {
  // 读不到的情况有三种、多半要在命令行里处理：首页跳到监控告警页，那里显示三种情况的处理说明（alertNote）。
  if (readonlyKeyMissing(object)) return { href: "#/ops/monitor", label: "查看处理办法" };
  if (object?.type === "host") return { href: hostHref(object.host_id), label: "查看主机" };
  if (object?.type === "dataset") {
    switch (object.producer) {
      case "factor":
        return { href: "#/factor/tasks?tab=results", label: "查看因子结果" };
      case "strategy":
        return { href: "#/strategy/tasks", label: "查看策略实例" };
      case "trade":
        return { href: "#/trading/live", label: "查看交易账户" };
      default:
        return collectorDatasetLink(object);
    }
  }
  switch (object?.kind) {
    case "kline_freshness":
    case "market_canary":
      return COLLECTOR_RESULTS;
    case "attention":
      // 下架处理的两种关注项去下架名单页（上段是下架处理记录）
      if (object.component_id === "trade" && object.attention_kind?.startsWith("delist_"))
        return { href: "#/trading/blocked", label: "查看下架处理" };
      if (object.component_id === "trade") return { href: "#/trading/live", label: "查看交易账户" };
      break;
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

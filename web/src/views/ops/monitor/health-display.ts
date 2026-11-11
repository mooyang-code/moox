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

// 主机页只按登记的 host_id 定位；历史曲线用的采集器由页面按该主机最近上报的采集器选择。
export function hostHref(hostID?: string) {
  return "#/ops/hosts?" + new URLSearchParams({ tab: "monitor", ...(hostID ? { host_id: hostID } : {}) });
}

const COLLECTOR_RESULTS = { href: "#/collector/tasks?tab=results", label: "查看采集结果" };

function readonlyKeyMissing(object?: HealthObject): boolean {
  return object?.kind === "collector_attention" && object.attention_kind === "delist_readonly_key_missing";
}

/** 告警的处理说明：几段文字，外加可选的处理入口。 */
export interface AlertNote {
  lines: string[];
  /** 说明里某一种情况能在页面上处理时给出入口；监控页用它代替默认的跳转链接。 */
  link?: { href: string; label: string };
}

/**
 * 主要靠命令行处理、要先看说明的告警：返回处理说明；其余返回 null。
 * 缺币安只读 Key：Collector 只读固定 ID exchange-binance-production-readonly，读不到分三种情况（被禁用、没有、在管理台删除过），
 * 告警分不出是哪一种，一次写全。密钥页新增的 ID 是随机的，补不上这把 Key；setup apply 不重建在管理台删除过的密钥（见 admin 的 setup/exchange_keys.go）。
 */
export function alertNote(object?: HealthObject): AlertNote | null {
  if (!readonlyKeyMissing(object)) return null;
  return {
    lines: [
      "已有这把 Key 但被禁用：到密钥管理页把“Binance production readonly”启用。",
      "还没有这把 Key：运行 moox-cli config edit，在“交易所密钥”里添加一把币安、正式环境的密钥，名称填 readonly，勾选“只读密钥”，再运行 moox-cli setup apply 写入。",
      "在管理台删除过这把 Key：setup apply 不会重建删除过的密钥，换一个名称也不行（Collector 只读固定 ID exchange-binance-production-readonly），目前补不回来。",
      "补上之前，现货下架只能手工登记：运行 moox-cli collector delist register，或在事件中心“事件”页签的“标的即将下架”一行点“手工登记”。"
    ],
    link: { href: "#/settings/secrets", label: "已禁用的话到密钥管理页启用" }
  };
}

/**
 * 事件中心关注项按种类跳转：通知失败看失败的执行记录；其余（含发现下架）看全部执行记录。
 * 系统动作处理失败不带失败筛选：它还包括记录未结束时有步骤失败、连续暂时失败、取消后善后未完成、下架后留有零头，
 * 这些记录的状态不是“失败”。
 */
function eventCenterLink(kind?: string): { href: string; label: string } {
  switch (kind) {
    case "execution_failed":
      return { href: "#/events?tab=executions&status=failed", label: "查看失败的执行记录" };
    default:
      return { href: "#/events?tab=executions", label: "查看执行记录" };
  }
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

/** 告警对象的处理入口：页面地址与链接文字。同一类关注项里的具体情况按 Monitor 给出的 attention_kind 区分。 */
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
    case "market_fetch":
      return COLLECTOR_RESULTS;
    case "data_delivery":
      return { href: "#/ops/storage/nodes", label: "查看存储节点" };
    case "balance":
    case "trade_attention":
      return { href: "#/trading/live", label: "查看交易账户" };
    case "event_center_attention":
      return eventCenterLink(object.attention_kind);
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

import { callControl } from "@/api/admin/http";
import type { ArrivalHistory, HealthOverview, LogBucket, LogRecord, NotificationChannel } from "./types";

const SERVICE = "monitor";

export const monitorApi = {
  /** 后台轮询传 { silentError: true }：失败不弹全局提示，由页面自己显示。 */
  getOverview(req: { space_id?: string } = {}, options: { silentError?: boolean } = {}) {
    return callControl<typeof req, { overview?: HealthOverview }>(SERVICE, "GetHealthOverview", req, options);
  },
  /** 列出全部推送渠道（含完整 Webhook URL）。 */
  listNotificationChannels() {
    return callControl<Record<string, never>, { channels?: NotificationChannel[] }>(SERVICE, "ListNotificationChannels", {});
  },
  addNotificationChannel(req: { channel_type: string; webhook_url: string; secret?: string }) {
    return callControl<typeof req, { channel?: NotificationChannel }>(SERVICE, "AddNotificationChannel", req);
  },
  deleteNotificationChannel(req: { channel_id: string }) {
    return callControl<typeof req, Record<string, never>>(SERVICE, "DeleteNotificationChannel", req);
  },
  /** 一条数据链路（源数据集 + 周期）某一天各环节逐根 K 线的到达延迟；day 按市场时区，空为今天。 */
  queryArrivalHistory(req: { space_id: string; dataset_id: string; freq: string; day?: string; task_id?: string }) {
    return callControl<typeof req, ArrivalHistory>(SERVICE, "QueryPipelineArrivalHistory", req);
  },
  /** 重要日志（各服务的 Error 及以上日志和关键事件），新的在前；before_id 传上一页最后一条的 id 向更早翻页。 */
  queryLogRecords(req: {
    host_id?: string;
    component?: string;
    level?: string;
    keyword?: string;
    start_at?: string;
    end_at?: string;
    before_id?: number | string;
    limit?: number;
    /** 另外返回按小时的条数分布（同样的条件）；没有给 start_at 时只统计最近 7 天。 */
    with_buckets?: boolean;
  }) {
    return callControl<typeof req, { records?: LogRecord[]; has_more?: boolean; buckets?: LogBucket[] }>(
      SERVICE,
      "QueryLogRecords",
      req
    );
  }
};

export type * from "./types";

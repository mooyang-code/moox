import { callControl } from "@/api/admin/http";
import type { ArrivalHistory, HealthOverview, NotificationChannel } from "./types";

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
  addNotificationChannel(req: { channel_type: string; webhook_url: string }) {
    return callControl<typeof req, { channel?: NotificationChannel }>(SERVICE, "AddNotificationChannel", req);
  },
  deleteNotificationChannel(req: { channel_id: string }) {
    return callControl<typeof req, Record<string, never>>(SERVICE, "DeleteNotificationChannel", req);
  },
  /** 一条数据链路（源数据集 + 周期）某一天各环节逐根 K 线的到达延迟；day 按市场时区，空为今天。 */
  queryArrivalHistory(req: { space_id: string; dataset_id: string; freq: string; day?: string; task_id?: string }) {
    return callControl<typeof req, ArrivalHistory>(SERVICE, "QueryPipelineArrivalHistory", req);
  }
};

export type * from "./types";

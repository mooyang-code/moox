import { callControl } from "@/api/admin/http";
import type { ArrivalHistory, HealthOverview, NotificationChannel } from "./types";

const SERVICE = "monitor";

export const monitorApi = {
  getOverview(req: { space_id?: string } = {}) {
    return callControl<typeof req, { overview?: HealthOverview }>(SERVICE, "GetHealthOverview", req);
  },
  /** 列出全部推送渠道（含完整 Webhook URL，只有全局管理员能调用）。 */
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

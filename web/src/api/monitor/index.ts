import { callControl } from "@/api/admin/http";
import type { HealthOverview, NotificationChannel } from "./types";

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
  }
};

export type * from "./types";

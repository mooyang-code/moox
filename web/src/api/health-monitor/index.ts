import { callControl } from "@/api/admin/http";
import type { HealthOverview, NotificationChannelResponse } from "./types";

const SERVICE = "moox_monitor";

export const healthMonitorApi = {
  getOverview(req: { space_id?: string } = {}) {
    return callControl<typeof req, { overview?: HealthOverview }>(SERVICE, "GetHealthOverview", req);
  },
  getNotification() {
    return callControl<Record<string, never>, NotificationChannelResponse>(SERVICE, "GetNotificationChannel", {});
  },
  updateNotification(req: { channel_type: string; webhook_url: string }) {
    return callControl<typeof req, NotificationChannelResponse>(SERVICE, "UpdateNotificationChannel", req);
  }
};

export type { HealthAlert, HealthInstance, HealthItem, HealthOverview, NotificationChannelResponse, NotificationChannelSetting } from "./types";

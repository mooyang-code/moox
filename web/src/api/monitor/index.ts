import { callControl } from "@/api/admin/http";
import type { HealthOverview, NotificationChannelResponse } from "./types";

const SERVICE = "monitor";

export const monitorApi = {
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

export type {
  HealthAlert,
  HealthBusinessCheck,
  HealthComponent,
  HealthHost,
  HealthNotification,
  HealthOverview,
  HealthPipelineDataset,
  HealthPipelineStage,
  HealthProbe,
  HealthReporter,
  HealthStatus,
  HealthSummary,
  HealthTarget,
  HealthUnregistered,
  NotificationChannelResponse,
  NotificationChannelSetting,
  WireInt64
} from "./types";

import { callControl } from "@/api/admin/http";
import type {
  AlertEvent,
  AlertRule,
  CheckResult,
  MonitorCheck,
  MonitorOverview,
  PageReq,
  PageResult,
  WebhookChannel
} from "./types";

const service = "moox_monitor";

export const monitorApi = {
  listChecks(req: { space_id?: string; group_name?: string; source?: string; page?: PageReq } = {}) {
    return callControl<typeof req, { checks?: MonitorCheck[]; page_result?: PageResult }>(service, "ListChecks", req);
  },
  getCheck(req: { space_id?: string; check_id: string }) {
    return callControl<typeof req, { check?: MonitorCheck }>(service, "GetCheck", req);
  },
  createCheck(check: MonitorCheck) {
    return callControl<{ check: MonitorCheck }, { check?: MonitorCheck }>(service, "CreateCheck", { check });
  },
  updateCheck(check: MonitorCheck) {
    return callControl<{ check: MonitorCheck }, { check?: MonitorCheck }>(service, "UpdateCheck", { check });
  },
  deleteCheck(req: { space_id?: string; check_id: string }) {
    return callControl<typeof req, Record<string, never>>(service, "DeleteCheck", req);
  },
  runCheckOnce(req: { space_id?: string; check_id: string }) {
    return callControl<typeof req, { result?: CheckResult }>(service, "RunCheckOnce", req);
  },
  listResults(req: { space_id?: string; check_id: string; limit?: number }) {
    return callControl<typeof req, { results?: CheckResult[] }>(service, "ListResults", req);
  },
  getOverview(req: { space_id?: string } = {}) {
    return callControl<typeof req, { overview?: MonitorOverview }>(service, "GetOverview", req);
  },
  listWebhookChannels(req: { space_id?: string } = {}) {
    return callControl<typeof req, { channels?: WebhookChannel[] }>(service, "ListWebhookChannels", req);
  },
  createWebhookChannel(channel: WebhookChannel) {
    return callControl<{ channel: WebhookChannel }, { channel?: WebhookChannel }>(service, "CreateWebhookChannel", { channel });
  },
  updateWebhookChannel(channel: WebhookChannel) {
    return callControl<{ channel: WebhookChannel }, { channel?: WebhookChannel }>(service, "UpdateWebhookChannel", { channel });
  },
  deleteWebhookChannel(req: { space_id?: string; webhook_id: string }) {
    return callControl<typeof req, Record<string, never>>(service, "DeleteWebhookChannel", req);
  },
  listAlertRules(req: { space_id?: string; check_id?: string } = {}) {
    return callControl<typeof req, { rules?: AlertRule[] }>(service, "ListAlertRules", req);
  },
  createAlertRule(rule: AlertRule) {
    return callControl<{ rule: AlertRule }, { rule?: AlertRule }>(service, "CreateAlertRule", { rule });
  },
  updateAlertRule(rule: AlertRule) {
    return callControl<{ rule: AlertRule }, { rule?: AlertRule }>(service, "UpdateAlertRule", { rule });
  },
  deleteAlertRule(req: { space_id?: string; rule_id: string }) {
    return callControl<typeof req, Record<string, never>>(service, "DeleteAlertRule", req);
  },
  listAlertEvents(req: { space_id?: string; limit?: number } = {}) {
    return callControl<typeof req, { events?: AlertEvent[] }>(service, "ListAlertEvents", req);
  },
  syncSystemChecks() {
    return callControl<Record<string, never>, { synced?: number }>(service, "SyncSystemChecks", {});
  }
};

export * from "./types";

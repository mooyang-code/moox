import { callControl } from '@/api/admin/http';
import {
  METRIC_HISTORY_LIMIT,
  METRIC_PREVIEW_LIMIT,
  METRIC_RULE_LIMIT,
  METRIC_SERIES_LIMIT,
  type MetricHistoryPoint,
  type MetricLatestPoint,
  type MetricNameInfo,
  type MetricRule,
  type MetricRuleEvaluation,
  type MetricRuleState,
  type MetricSeriesInfo,
  type MetricServiceInfo,
  type PageRequest,
  type PageResult,
  type WebhookChannel,
} from './types';

// The admin gateway keeps the monitor deployment under this stable service id.
export const METRIC_MONITOR_SERVICE = 'moox_monitor';

function boundedPage(page: PageRequest | undefined, max: number): PageRequest | undefined {
  if (!page) return undefined;
  return {
    page: Math.max(1, page.page || 1),
    size: Math.min(max, Math.max(1, page.size || max)),
  };
}

function boundedLimit(limit: number | undefined, max: number, fallback: number): number {
  return Math.min(max, Math.max(1, Math.floor(limit || fallback)));
}

export const metricMonitorApi = {
  listMetricServices(req: { space_id?: string; page?: PageRequest } = {}) {
    return callControl<typeof req, { services?: MetricServiceInfo[]; page_result?: PageResult }>(METRIC_MONITOR_SERVICE, 'ListMetricServices', {
      ...req,
      page: boundedPage(req.page, METRIC_SERIES_LIMIT),
    });
  },

  listMetricNames(req: { space_id?: string; service_name?: string; page?: PageRequest } = {}) {
    return callControl<typeof req, { names?: MetricNameInfo[]; page_result?: PageResult }>(METRIC_MONITOR_SERVICE, 'ListMetricNames', {
      ...req,
      page: boundedPage(req.page, METRIC_SERIES_LIMIT),
    });
  },

  listMetricSeries(req: { space_id?: string; service_name?: string; metric_name?: string; labels_json?: string; page?: PageRequest } = {}) {
    return callControl<typeof req, { series?: MetricSeriesInfo[]; page_result?: PageResult }>(METRIC_MONITOR_SERVICE, 'ListMetricSeries', {
      ...req,
      page: boundedPage(req.page, METRIC_SERIES_LIMIT),
    });
  },

  getMetricLatest(req: { space_id?: string; series_id: string }) {
    return callControl<typeof req, { latest?: MetricLatestPoint }>(METRIC_MONITOR_SERVICE, 'GetMetricLatest', req);
  },

  queryMetricHistory(req: {
    space_id?: string;
    series_id?: string;
    service_name?: string;
    metric_name?: string;
    start_at?: string;
    end_at?: string;
    order?: number;
    limit?: number;
    labels_json?: string;
  }) {
    return callControl<typeof req, { points?: MetricHistoryPoint[] }>(METRIC_MONITOR_SERVICE, 'QueryMetricHistory', {
      ...req,
      limit: boundedLimit(req.limit, METRIC_HISTORY_LIMIT, METRIC_HISTORY_LIMIT),
    });
  },

  listMetricRules(req: { space_id?: string; enabled_only?: boolean; page?: PageRequest } = {}) {
    return callControl<typeof req, { rules?: MetricRule[]; page_result?: PageResult }>(METRIC_MONITOR_SERVICE, 'ListMetricRules', {
      ...req,
      page: boundedPage(req.page, METRIC_RULE_LIMIT),
    });
  },

  getMetricRule(req: { space_id?: string; rule_id: string }) {
    return callControl<typeof req, { rule?: MetricRule }>(METRIC_MONITOR_SERVICE, 'GetMetricRule', req);
  },

  createMetricRule(rule: MetricRule) {
    return callControl<{ rule: MetricRule }, { rule?: MetricRule }>(METRIC_MONITOR_SERVICE, 'CreateMetricRule', { rule });
  },

  updateMetricRule(rule: MetricRule) {
    return callControl<{ rule: MetricRule }, { rule?: MetricRule }>(METRIC_MONITOR_SERVICE, 'UpdateMetricRule', { rule });
  },

  deleteMetricRule(req: { space_id?: string; rule_id: string }) {
    return callControl<typeof req, Record<string, never>>(METRIC_MONITOR_SERVICE, 'DeleteMetricRule', req);
  },

  previewMetricRule(rule: MetricRule, limit = METRIC_PREVIEW_LIMIT) {
    return callControl<{ rule: MetricRule; limit: number }, { evaluation?: MetricRuleEvaluation }>(METRIC_MONITOR_SERVICE, 'PreviewMetricRule', {
      rule,
      limit: boundedLimit(limit, METRIC_PREVIEW_LIMIT, METRIC_PREVIEW_LIMIT),
    });
  },

  listMetricRuleEvaluations(req: { space_id?: string; rule_id?: string; page?: PageRequest } = {}) {
    return callControl<typeof req, { evaluations?: MetricRuleEvaluation[]; page_result?: PageResult }>(METRIC_MONITOR_SERVICE, 'ListMetricRuleEvaluations', {
      ...req,
      page: boundedPage(req.page, METRIC_RULE_LIMIT),
    });
  },

  getMetricRuleState(req: { space_id?: string; rule_id: string }) {
    return callControl<typeof req, { state?: MetricRuleState }>(METRIC_MONITOR_SERVICE, 'GetMetricRuleState', req);
  },

  listWebhookChannels(req: { space_id?: string } = {}) {
    return callControl<typeof req, { channels?: WebhookChannel[] }>(METRIC_MONITOR_SERVICE, 'ListWebhookChannels', req);
  },
};

export * from './types';

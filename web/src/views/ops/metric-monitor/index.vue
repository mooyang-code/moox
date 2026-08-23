<template>
  <div class="metric-monitor-page">
    <header v-if="!embedded" class="page-head">
      <div v-if="!embedded">
        <h2>应用指标</h2>
        <span>先看哪些服务需要关注，再定位到具体指标和时间。</span>
      </div>
    </header>

    <a-tabs v-model:active-key="activeTab" type="rounded" size="small" class="metric-subtabs">
      <a-tab-pane key="explorer" title="指标看板" />
      <a-tab-pane key="rules" title="告警规则" />
    </a-tabs>

    <div class="metric-tab-content">
      <section v-if="activeTab === 'explorer'" class="metric-tab-panel">
        <section class="filter-band">
          <a-button type="primary" status="success" aria-label="新建指标" @click="openCreateRule">
            <template #icon><icon-plus /></template>
            新建告警规则
          </a-button>
          <a-select
            v-model="selectedService"
            allow-clear
            placeholder="服务"
            :loading="catalogLoading"
            @change="onServiceChange"
            style="width: 220px"
          >
            <a-option v-for="service in serviceOptions" :key="service" :value="service">{{
              serviceDisplayName(service)
            }}</a-option>
          </a-select>
          <a-select v-model="selectedInstance" allow-clear placeholder="实例" @change="refreshSeriesData" style="width: 220px">
            <a-option v-for="instance in instanceOptions" :key="instance" :value="instance">{{ instance }}</a-option>
          </a-select>
          <a-select
            v-model="selectedMetric"
            allow-clear
            placeholder="指标"
            :loading="catalogLoading"
            @change="onMetricChange"
            style="width: 280px"
          >
            <a-option v-for="metric in metricOptions" :key="metric.metric_name" :value="metric.metric_name">
              {{ metricDisplayName(metric.metric_name) }} <span class="option-count">({{ metric.series_count || 0 }} 条)</span>
            </a-option>
          </a-select>
          <a-input
            v-model="labelsFilter"
            allow-clear
            placeholder="维度过滤（空间、Dataset、频率）"
            style="width: 240px"
            @press-enter="refreshSeriesData"
          />
          <a-button @click="refreshSeriesData">查询</a-button>
        </section>

        <section v-if="!catalogLoading && !serviceOptions.length" class="state-band">
          <a-empty description="暂无已上报服务" />
        </section>
        <section v-else class="explorer-grid">
          <section class="metric-summary" aria-label="指标摘要">
            <div class="summary-item summary-ok">
              <span>状态正常</span>
              <strong>{{ healthyCount }}</strong>
              <small>条指标序列</small>
            </div>
            <div class="summary-item summary-warning" title="陈旧数据会被标记为需要关注">
              <span>需要关注</span>
              <strong>{{ staleCount }}</strong>
              <small>条指标序列</small>
            </div>
            <div class="summary-item summary-muted">
              <span>暂无数据</span>
              <strong>{{ noDataCount }}</strong>
              <small>条指标序列</small>
            </div>
            <div class="summary-item summary-info">
              <span>当前服务</span>
              <strong>{{ serviceCount }}</strong>
              <small>个服务</small>
            </div>
          </section>

          <div class="latest-panel">
            <div class="section-head">
              <div>
                <strong>当前状态</strong>
                <span v-if="seriesTotal > MAX_DISPLAY_SERIES" class="cardinality-note"
                  >已限制 {{ MAX_DISPLAY_SERIES }} 条，匹配总数 {{ seriesTotal }}</span
                >
              </div>
              <a-tag v-if="staleCount" color="orange">{{ staleCount }} 条指标需关注</a-tag>
            </div>
            <a-table
              row-key="series_id"
              size="small"
              :bordered="{ cell: true }"
              :loading="latestLoading"
              :data="displayLatestRows"
              :pagination="false"
              :scroll="{ x: 'max-content', y: 430 }"
            >
              <template #columns>
                <a-table-column title="服务" :width="140">
                  <template #cell="{ record }">{{ serviceDisplayName(record.service_name || selectedService) }}</template>
                </a-table-column>
                <a-table-column title="指标" :width="240">
                  <template #cell="{ record }">
                    <span class="metric-cell" :title="record.metric_name || ''">
                      <strong>{{ metricDisplayName(record.metric_name) }}</strong>
                    </span>
                  </template>
                </a-table-column>
                <a-table-column title="当前值" :width="150">
                  <template #cell="{ record }">{{ metricValueDisplay(record.metric_name, record.value) }}</template>
                </a-table-column>
                <a-table-column title="状态" :width="110">
                  <template #cell="{ record }"
                    ><a-tag size="small" :color="statusColor(record)">{{ metricStatusText(record) }}</a-tag></template
                  >
                </a-table-column>
                <a-table-column title="更新时间" :width="190">
                  <template #cell="{ record }">{{ formatTime(record.observed_at) }}</template>
                </a-table-column>
                <a-table-column title="关键维度" :width="300">
                  <template #cell="{ record }">
                    <div v-if="metricDimensionSummary(record.labels_json).items.length" class="dimension-list">
                      <a-tag
                        v-for="dimension in metricDimensionSummary(record.labels_json).items"
                        :key="`${dimension.key}-${dimension.value}`"
                        size="small"
                        class="dimension-tag"
                        >{{ dimension.label }}：{{ dimension.value }}</a-tag
                      >
                      <span v-if="metricDimensionSummary(record.labels_json).overflow" class="dimension-more"
                        >+{{ metricDimensionSummary(record.labels_json).overflow }}</span
                      >
                    </div>
                    <span v-else class="muted-cell">暂无维度</span>
                  </template>
                </a-table-column>
                <a-table-column title="操作" :width="80" fixed="right">
                  <template #cell="{ record }">
                    <a-button size="mini" type="text" @click="openMetricDetail(record)">详情</a-button>
                  </template>
                </a-table-column>
              </template>
            </a-table>
            <div v-if="!latestLoading && !latestRows.length" class="inline-empty">
              {{ selectedSeries.length ? "当前筛选范围暂无可用数据" : "没有匹配的指标序列" }}
            </div>
          </div>

          <div class="chart-panel">
            <div class="section-head">
              <div>
                <strong>历史趋势</strong>
                <span class="section-meta">近 1 小时 · 最多 {{ MAX_CHART_SERIES }} 条序列</span>
              </div>
            </div>
            <metric-chart :series="chartPoints" :loading="historyLoading" />
          </div>
        </section>
      </section>

      <section v-else class="metric-tab-panel">
        <section class="rules-panel">
          <div class="section-head">
            <div><strong>指标告警规则</strong><span class="section-meta">平面 A-H 条件，仅支持单层 AND / OR</span></div>
            <a-button type="primary" status="success" size="small" @click="openCreateRule">新增规则</a-button>
          </div>
          <a-table
            row-key="rule_id"
            size="small"
            :loading="rulesLoading"
            :data="rules"
            :pagination="false"
            :scroll="{ x: 'max-content' }"
          >
            <template #columns>
              <a-table-column title="名称" :width="220">
                <template #cell="{ record }"
                  ><div class="rule-name">
                    <strong>{{ record.name }}</strong
                    ><span>{{ record.rule_id }}</span>
                  </div></template
                >
              </a-table-column>
              <a-table-column title="条件" :width="90"
                ><template #cell="{ record }">{{ record.conditions?.length || 0 }} 条</template></a-table-column
              >
              <a-table-column title="连接" :width="90"
                ><template #cell="{ record }">{{ connectorText(record.connector) }}</template></a-table-column
              >
              <a-table-column title="状态" :width="150">
                <template #cell="{ record }"
                  ><a-space
                    ><a-tag size="small" :color="record.enabled ? 'green' : 'gray'">{{ record.enabled ? "启用" : "停用" }}</a-tag
                    ><a-tag
                      v-if="ruleState(record.rule_id)?.status"
                      size="small"
                      :color="ruleStatusColor(ruleState(record.rule_id)?.status)"
                      >{{ ruleStatusText(ruleState(record.rule_id)?.status) }}</a-tag
                    ></a-space
                  ></template
                >
              </a-table-column>
              <a-table-column title="连续计数" :width="110"
                ><template #cell="{ record }"
                  >{{ ruleState(record.rule_id)?.trigger_count || 0 }} / {{ record.consecutive_trigger_count }}</template
                ></a-table-column
              >
              <a-table-column title="最近评估" :width="190"
                ><template #cell="{ record }">{{
                  formatTime(ruleState(record.rule_id)?.last_evaluated_at)
                }}</template></a-table-column
              >
              <a-table-column title="操作" :width="230" align="center">
                <template #cell="{ record }"
                  ><a-space
                    ><a-button size="mini" type="text" @click="openEditRule(record)">编辑</a-button
                    ><a-button size="mini" type="text" @click="openEvaluations(record)">评估记录</a-button
                    ><a-popconfirm content="确认删除该规则？" @ok="removeRule(record)"
                      ><a-button size="mini" type="text" status="danger">删除</a-button></a-popconfirm
                    ></a-space
                  ></template
                >
              </a-table-column>
            </template>
          </a-table>
          <div v-if="!rulesLoading && !rules.length" class="inline-empty">暂无指标告警规则</div>
        </section>
      </section>
    </div>

    <a-drawer
      v-model:visible="ruleDrawerVisible"
      width="1060px"
      :title="editingRule?.rule_id ? '编辑指标告警规则' : '新建指标告警规则'"
      :footer="false"
      unmount-on-close
    >
      <metric-rule-editor :rule="editingRule" :webhooks="webhooks" @saved="onRuleSaved" @cancel="ruleDrawerVisible = false" />
    </a-drawer>

    <a-drawer v-model:visible="evaluationDrawerVisible" width="900px" title="评估记录" :footer="false" unmount-on-close>
      <a-table size="small" :loading="evaluationLoading" :data="evaluations" :pagination="false" :scroll="{ x: 'max-content' }">
        <template #columns>
          <a-table-column title="时间" :width="190"
            ><template #cell="{ record }">{{ formatTime(record.evaluated_at) }}</template></a-table-column
          >
          <a-table-column title="状态" :width="110"
            ><template #cell="{ record }"
              ><a-tag :color="ruleStatusColor(record.status)">{{ ruleStatusText(record.status) }}</a-tag></template
            ></a-table-column
          >
          <a-table-column title="结果" :width="90"
            ><template #cell="{ record }">{{ record.result ? "触发" : "正常" }}</template></a-table-column
          >
          <a-table-column title="条件结果" :width="440"
            ><template #cell="{ record }">{{ evaluationSummary(record) }}</template></a-table-column
          >
        </template>
      </a-table>
      <div v-if="!evaluationLoading && !evaluations.length" class="inline-empty">暂无评估记录</div>
    </a-drawer>

    <a-drawer v-model:visible="metricDetailVisible" width="520px" title="指标详情" :footer="false" unmount-on-close>
      <template v-if="selectedMetricRow">
        <a-descriptions :column="1" bordered size="small">
          <a-descriptions-item label="服务">{{
            serviceDisplayName(selectedMetricRow.service_name || selectedService)
          }}</a-descriptions-item>
          <a-descriptions-item label="指标">{{ metricDisplayName(selectedMetricRow.metric_name) }}</a-descriptions-item>
          <a-descriptions-item label="当前值">{{
            metricValueDisplay(selectedMetricRow.metric_name, selectedMetricRow.value)
          }}</a-descriptions-item>
          <a-descriptions-item label="状态">
            <a-tag size="small" :color="statusColor(selectedMetricRow)">{{ metricStatusText(selectedMetricRow) }}</a-tag>
            <span class="detail-note">{{ metricStatusReason(selectedMetricRow) }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="更新时间">{{ formatTime(selectedMetricRow.observed_at) }}</a-descriptions-item>
          <a-descriptions-item label="实例">{{ selectedMetricRow.instance_id || "-" }}</a-descriptions-item>
          <a-descriptions-item label="关键维度">
            <div v-if="parseMetricLabels(selectedMetricRow.labels_json).length" class="detail-dimensions">
              <a-tag
                v-for="dimension in parseMetricLabels(selectedMetricRow.labels_json)"
                :key="`${dimension.key}-${dimension.value}`"
                size="small"
              >
                {{ dimension.label }}：{{ dimension.value }}
              </a-tag>
            </div>
            <span v-else>-</span>
          </a-descriptions-item>
        </a-descriptions>
        <a-collapse class="advanced-details">
          <a-collapse-item key="raw" header="高级信息">
            <div class="raw-field">
              <span>原始指标名</span><code>{{ selectedMetricRow.metric_name || "-" }}</code>
            </div>
            <div class="raw-field">
              <span>序列 ID</span><code>{{ selectedMetricRow.series_id || "-" }}</code>
            </div>
            <pre class="raw-json">{{ selectedMetricRow.labels_json || "{}" }}</pre>
          </a-collapse-item>
        </a-collapse>
      </template>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import MetricChart, { type ChartPoint } from "./metric-chart.vue";
import MetricRuleEditor from "./metric-rule-editor.vue";
import { metricMonitorApi } from "@/api/metric-monitor";
import type {
  MetricHistoryPoint,
  MetricLatestPoint,
  MetricNameInfo,
  MetricRule,
  MetricRuleEvaluation,
  MetricSeriesInfo,
  MetricServiceInfo,
  MetricRuleState,
  WebhookChannel
} from "@/api/metric-monitor/types";
import {
  metricDimensionSummary,
  metricDisplayName,
  metricStatusReason,
  metricStatusText,
  metricValueDisplay,
  parseMetricLabels,
  serviceDisplayName
} from "./metric-display";

const props = defineProps<{ embedded?: boolean }>();
const embedded = computed(() => props.embedded === true);

const MAX_DISPLAY_SERIES = 50;
const MAX_CHART_SERIES = 10;
const activeTab = ref("explorer");
const loading = ref(false);
const catalogLoading = ref(false);
const latestLoading = ref(false);
const historyLoading = ref(false);
const rulesLoading = ref(false);
const evaluationLoading = ref(false);
const services = ref<MetricServiceInfo[]>([]);
const metricOptions = ref<MetricNameInfo[]>([]);
const series = ref<MetricSeriesInfo[]>([]);
const latestRows = ref<MetricLatestPoint[]>([]);
const historyBySeries = ref<Record<string, MetricHistoryPoint[]>>({});
const rules = ref<MetricRule[]>([]);
const ruleStates = ref<Record<string, MetricRuleState>>({});
const webhooks = ref<WebhookChannel[]>([]);
const evaluations = ref<MetricRuleEvaluation[]>([]);
const selectedService = ref("");
const selectedInstance = ref("");
const selectedMetric = ref("");
const labelsFilter = ref("");
const ruleDrawerVisible = ref(false);
const evaluationDrawerVisible = ref(false);
const metricDetailVisible = ref(false);
const editingRule = ref<MetricRule>();
const selectedMetricRow = ref<MetricLatestPoint>();

const serviceOptions = computed(() =>
  [...new Set(services.value.map(item => item.service_name).filter(Boolean) as string[])].sort()
);
const instanceOptions = computed(() =>
  [
    ...new Set(
      services.value
        .filter(item => !selectedService.value || item.service_name === selectedService.value)
        .map(item => item.instance_id)
        .filter(Boolean) as string[]
    )
  ].sort()
);
const selectedSeries = computed(() =>
  series.value
    .filter(
      item =>
        (!selectedInstance.value || item.instance_id === selectedInstance.value) &&
        (!labelsFilter.value || (item.labels_json || "").toLowerCase().includes(labelsFilter.value.toLowerCase()))
    )
    .slice(0, MAX_DISPLAY_SERIES)
);
const seriesTotal = computed(
  () =>
    series.value.filter(
      item =>
        (!selectedInstance.value || item.instance_id === selectedInstance.value) &&
        (!labelsFilter.value || (item.labels_json || "").toLowerCase().includes(labelsFilter.value.toLowerCase()))
    ).length
);
const staleCount = computed(() => latestRows.value.filter(item => isStale(item)).length);
const healthyCount = computed(() => latestRows.value.filter(item => !isStale(item)).length);
const noDataCount = computed(() => Math.max(0, selectedSeries.value.length - latestRows.value.length));
const serviceCount = computed(() => {
  const visibleServices = new Set(
    latestRows.value.map(item => item.service_name || selectedService.value).filter(Boolean) as string[]
  );
  return visibleServices.size || (selectedService.value ? 1 : serviceOptions.value.length);
});
const displayLatestRows = computed(() =>
  [...latestRows.value].sort((left, right) => Number(isStale(right)) - Number(isStale(left)))
);
const chartPoints = computed<ChartPoint[]>(() =>
  Object.entries(historyBySeries.value).flatMap(([seriesId, points]) =>
    points.map(point => ({
      time: formatTime(point.observed_at),
      value: point.value || 0,
      series: chartSeriesLabel(seriesId)
    }))
  )
);

function normalizeRule(rule?: MetricRule): MetricRule {
  return JSON.parse(
    JSON.stringify(
      rule || {
        name: "",
        conditions: [],
        connector: 1,
        consecutive_trigger_count: 3,
        consecutive_recovery_count: 1,
        evaluation_interval_seconds: 30,
        webhook_ids: [],
        enabled: true
      }
    )
  ) as MetricRule;
}

function notifyError(reason: unknown, fallback: string) {
  const message = reason instanceof Error ? reason.message : fallback;
  if (!/metrics catalog is unavailable/i.test(message)) Message.error(message);
}

async function loadCatalog() {
  catalogLoading.value = true;
  try {
    const response = await metricMonitorApi.listMetricServices({ page: { page: 1, size: 200 } });
    services.value = response.services || [];
    if (!selectedService.value || !serviceOptions.value.includes(selectedService.value))
      selectedService.value = serviceOptions.value[0] || "";
    await loadNames();
  } catch (error) {
    notifyError(error, "服务指标目录加载失败");
  } finally {
    catalogLoading.value = false;
  }
}

async function loadNames() {
  metricOptions.value = [];
  if (!selectedService.value) {
    selectedMetric.value = "";
    series.value = [];
    return;
  }
  const response = await metricMonitorApi.listMetricNames({ service_name: selectedService.value, page: { page: 1, size: 200 } });
  metricOptions.value = response.names || [];
  if (!selectedMetric.value || !metricOptions.value.some(item => item.metric_name === selectedMetric.value))
    selectedMetric.value = metricOptions.value[0]?.metric_name || "";
  await loadSeries();
}

async function loadSeries() {
  series.value = [];
  if (!selectedService.value || !selectedMetric.value) {
    latestRows.value = [];
    historyBySeries.value = {};
    return;
  }
  try {
    const response = await metricMonitorApi.listMetricSeries({
      service_name: selectedService.value,
      metric_name: selectedMetric.value,
      page: { page: 1, size: MAX_DISPLAY_SERIES }
    });
    series.value = response.series || [];
    await refreshSeriesData();
  } catch (error) {
    notifyError(error, "指标序列加载失败");
  }
}

async function refreshSeriesData() {
  await Promise.all([loadLatest(), loadHistory()]);
}

async function loadLatest() {
  latestLoading.value = true;
  try {
    const responses = await Promise.allSettled(
      selectedSeries.value
        .filter(item => item.series_id)
        .map(item => metricMonitorApi.getMetricLatest({ series_id: item.series_id as string }))
    );
    latestRows.value = responses
      .filter(
        (response): response is PromiseFulfilledResult<{ latest?: MetricLatestPoint }> =>
          response.status === "fulfilled" && !!response.value.latest
      )
      .map(response => response.value.latest as MetricLatestPoint);
    if (responses.some(response => response.status === "rejected")) Message.error("部分指标最新值加载失败，已保留可查询数据。");
  } finally {
    latestLoading.value = false;
  }
}

async function loadHistory() {
  historyLoading.value = true;
  const startAt = new Date(Date.now() - 60 * 60 * 1000).toISOString();
  try {
    const responses = await Promise.allSettled(
      selectedSeries.value
        .slice(0, MAX_CHART_SERIES)
        .filter(item => item.series_id)
        .map(item =>
          metricMonitorApi.queryMetricHistory({ series_id: item.series_id as string, start_at: startAt, order: 1, limit: 500 })
        )
    );
    const next: Record<string, MetricHistoryPoint[]> = {};
    responses.forEach((response, index) => {
      const id = selectedSeries.value[index]?.series_id;
      if (response.status === "fulfilled" && id) next[id] = response.value.points || [];
    });
    historyBySeries.value = next;
    if (responses.some(response => response.status === "rejected")) Message.error("部分历史指标加载失败，已保留可查询数据。");
  } catch (error) {
    notifyError(error, "历史数据加载失败");
  } finally {
    historyLoading.value = false;
  }
}

async function loadRules() {
  rulesLoading.value = true;
  try {
    const response = await metricMonitorApi.listMetricRules({ page: { page: 1, size: 100 } });
    rules.value = response.rules || [];
    const states = await Promise.allSettled(
      rules.value
        .filter(rule => rule.rule_id)
        .map(rule => metricMonitorApi.getMetricRuleState({ rule_id: rule.rule_id as string }))
    );
    ruleStates.value = Object.fromEntries(
      states
        .map((response, index) => [
          rules.value[index]?.rule_id,
          response.status === "fulfilled" ? response.value.state : undefined
        ])
        .filter((entry): entry is [string, MetricRuleState] => !!entry[0] && !!entry[1])
    );
  } catch (error) {
    notifyError(error, "告警规则加载失败");
  } finally {
    rulesLoading.value = false;
  }
}

async function loadWebhooks() {
  try {
    webhooks.value = (await metricMonitorApi.listWebhookChannels()).channels || [];
  } catch (reason) {
    webhooks.value = [];
    notifyError(reason, "通知通道加载失败");
  }
}

async function refreshAll() {
  loading.value = true;
  try {
    await Promise.all([loadCatalog(), loadRules(), loadWebhooks()]);
    Message.success("指标数据已刷新");
  } finally {
    loading.value = false;
  }
}

async function onServiceChange() {
  selectedInstance.value = "";
  await loadNames();
}

async function onMetricChange() {
  await loadSeries();
}

function openCreateRule() {
  editingRule.value = normalizeRule();
  ruleDrawerVisible.value = true;
}

function openEditRule(rule: MetricRule) {
  editingRule.value = normalizeRule(rule);
  ruleDrawerVisible.value = true;
}

async function onRuleSaved() {
  ruleDrawerVisible.value = false;
  await loadRules();
}

async function removeRule(rule: MetricRule) {
  if (!rule.rule_id) return;
  await metricMonitorApi.deleteMetricRule({ rule_id: rule.rule_id });
  await loadRules();
}

async function openEvaluations(rule: MetricRule) {
  evaluationDrawerVisible.value = true;
  evaluationLoading.value = true;
  try {
    evaluations.value =
      (await metricMonitorApi.listMetricRuleEvaluations({ rule_id: rule.rule_id, page: { page: 1, size: 50 } })).evaluations ||
      [];
  } finally {
    evaluationLoading.value = false;
  }
}

function openMetricDetail(row: MetricLatestPoint) {
  selectedMetricRow.value = row;
  metricDetailVisible.value = true;
}

function ruleState(ruleId?: string) {
  return ruleId ? ruleStates.value[ruleId] : undefined;
}
function formatTime(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString();
}
function formatNumber(value?: number) {
  return value === undefined || value === null ? "-" : Number(value).toLocaleString(undefined, { maximumFractionDigits: 6 });
}
function isStale(row: MetricLatestPoint) {
  return !!row.stale || (!!row.observed_at && Date.now() - new Date(row.observed_at).valueOf() > 90_000);
}
function statusColor(row: MetricLatestPoint) {
  return isStale(row) ? "orange" : "green";
}
function chartSeriesLabel(seriesId: string) {
  const metadata = series.value.find(item => item.series_id === seriesId);
  if (!metadata) return metricDisplayName(selectedMetric.value);
  const dimensions = metricDimensionSummary(metadata.labels_json, 2).items.map(
    dimension => `${dimension.label}：${dimension.value}`
  );
  if (metadata.instance_id) dimensions.push(`实例：${metadata.instance_id}`);
  return [metricDisplayName(metadata.metric_name), ...dimensions].join(" · ");
}
function connectorText(value?: number) {
  return value === 2 ? "OR" : "AND";
}
function ruleStatusText(value?: number | string) {
  return value === 2 || value === "ALERT_STATUS_FIRING"
    ? "FIRING"
    : value === 3 || value === "ALERT_STATUS_RESOLVED"
      ? "RESOLVED"
      : "OK";
}
function ruleStatusColor(value?: number | string) {
  return ruleStatusText(value) === "FIRING" ? "red" : ruleStatusText(value) === "RESOLVED" ? "blue" : "green";
}
function evaluationSummary(evaluation: MetricRuleEvaluation) {
  return (evaluation.conditions || [])
    .map(
      item =>
        `${item.condition_id || "-"}:${item.has_data === false ? "无数据" : item.result ? "触发" : "正常"}(${formatNumber(item.value)})`
    )
    .join(" · ");
}

watch(selectedInstance, refreshSeriesData);
onMounted(refreshAll);
</script>

<style scoped lang="scss">
.metric-monitor-page {
  height: 100%;
  overflow-y: auto;
  padding: 0 0 var(--moox-space-5);
  color: var(--color-text-1);
}
.page-head,
.section-head,
.filter-band {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
}
.page-head {
  margin-bottom: var(--moox-space-2);
}
.page-head h2 {
  margin: 0 0 var(--moox-space-1);
  font-size: 20px;
  font-weight: 600;
}
.page-head span,
.section-meta,
.cardinality-note {
  color: var(--color-text-3);
  font-size: 12px;
}
.metric-subtabs {
  margin-bottom: var(--moox-space-3);
}
.metric-subtabs :deep(.arco-tabs-content) {
  display: none;
}
.metric-subtabs :deep(.arco-tabs-tab:first-child) {
  margin-left: 0;
}
.metric-subtabs :deep(.arco-tabs-tab) {
  border-radius: 4px;
}
.metric-subtabs :deep(.arco-tabs-tab-active) {
  color: rgb(var(--primary-6));
  background-color: var(--color-fill-2);
}
.filter-band {
  justify-content: flex-start;
  flex-wrap: wrap;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-2);
  padding: 0;
}
.option-count {
  color: var(--color-text-3);
}
.state-band,
.latest-panel,
.chart-panel,
.rules-panel {
  margin-top: var(--moox-space-2);
  padding: var(--moox-space-4);
  border: 1px solid var(--color-border-2);
  background: var(--color-bg-2);
}
.state-band {
  min-height: 220px;
  display: grid;
  place-items: center;
}
.explorer-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: var(--moox-space-4);
}
.metric-summary {
  display: grid;
  grid-column: 1 / -1;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--moox-space-3);
}
.summary-item {
  display: grid;
  grid-template-columns: 1fr auto;
  align-items: center;
  column-gap: var(--moox-space-2);
  min-height: 78px;
  padding: var(--moox-space-3) var(--moox-space-4);
  border: 1px solid var(--color-border-2);
  border-left: 3px solid var(--color-text-3);
  background: var(--color-bg-2);
}
.summary-item span,
.summary-item small {
  color: var(--color-text-3);
}
.summary-item span {
  font-size: 13px;
}
.summary-item strong {
  grid-row: span 2;
  color: var(--color-text-1);
  font-size: 26px;
  font-weight: 600;
  line-height: 1;
}
.summary-item small {
  font-size: 11px;
}
.summary-ok {
  border-left-color: rgb(var(--green-6));
}
.summary-warning {
  border-left-color: rgb(var(--orange-6));
}
.summary-muted {
  border-left-color: var(--color-text-4);
}
.summary-info {
  border-left-color: rgb(var(--primary-6));
}
.latest-panel,
.chart-panel {
  min-width: 0;
}
.section-head {
  margin-bottom: var(--moox-space-3);
}
.section-head > div {
  display: flex;
  align-items: baseline;
  gap: 10px;
}
.cardinality-note {
  color: var(--color-warning-6);
}
.inline-empty {
  padding: 28px;
  text-align: center;
  color: var(--color-text-3);
}
.metric-cell {
  display: inline-flex;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dimension-list,
.detail-dimensions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
}
.dimension-tag {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dimension-more,
.muted-cell,
.detail-note {
  color: var(--color-text-3);
  font-size: 12px;
}
.detail-note {
  margin-left: 8px;
}
.advanced-details {
  margin-top: var(--moox-space-4);
}
.raw-field {
  display: grid;
  grid-template-columns: 90px minmax(0, 1fr);
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-2);
  font-size: 12px;
}
.raw-field span {
  color: var(--color-text-3);
}
.raw-field code,
.raw-json {
  overflow-wrap: anywhere;
  word-break: break-word;
}
.raw-json {
  margin: var(--moox-space-3) 0 0;
  padding: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  background: var(--color-fill-1);
  color: var(--color-text-2);
  font-size: 11px;
  white-space: pre-wrap;
}
.rule-name {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.rule-name span {
  color: var(--color-text-3);
  font-size: 12px;
}
@media (max-width: 900px) {
  .metric-summary {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 560px) {
  .metric-summary {
    grid-template-columns: minmax(0, 1fr);
  }
  .summary-item {
    min-height: 64px;
  }
}
</style>

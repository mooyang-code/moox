<template>
  <div class="metric-monitor-page">
    <header class="page-head">
      <div v-if="!embedded">
        <h2>应用指标</h2>
        <span>服务实例主动上报的 Prometheus 指标与历史趋势。</span>
      </div>
      <a-space>
        <a-button @click="refreshAll" :loading="loading">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
        <a-button type="primary" @click="openCreateRule">
          <template #icon><icon-plus /></template>
          新建规则
        </a-button>
      </a-space>
    </header>

    <a-alert v-if="errorMessage" type="error" closable @close="errorMessage = ''">{{ errorMessage }}</a-alert>
    <a-alert v-if="partialState" type="warning" class="state-alert">部分序列暂不可用，已保留可查询数据。</a-alert>

    <a-tabs v-model:active-key="activeTab" type="rounded">
      <a-tab-pane key="explorer" title="指标看板">
        <section class="filter-band">
          <a-select v-model="selectedService" allow-clear placeholder="服务" :loading="catalogLoading" @change="onServiceChange" style="width: 220px">
            <a-option v-for="service in serviceOptions" :key="service" :value="service">{{ service }}</a-option>
          </a-select>
          <a-select v-model="selectedInstance" allow-clear placeholder="实例" @change="refreshSeriesData" style="width: 220px">
            <a-option v-for="instance in instanceOptions" :key="instance" :value="instance">{{ instance }}</a-option>
          </a-select>
          <a-select v-model="selectedMetric" allow-clear placeholder="指标" :loading="catalogLoading" @change="onMetricChange" style="width: 280px">
            <a-option v-for="metric in metricOptions" :key="metric.metric_name" :value="metric.metric_name">
              {{ metric.metric_name }} <span class="option-count">({{ metric.series_count || 0 }})</span>
            </a-option>
          </a-select>
          <a-input v-model="labelsFilter" allow-clear placeholder="标签文本过滤" style="width: 240px" @press-enter="refreshSeriesData" />
          <a-button @click="refreshSeriesData">查询</a-button>
        </section>

        <section v-if="!catalogLoading && !serviceOptions.length" class="state-band">
          <a-empty description="暂无已上报服务" />
        </section>
        <section v-else class="explorer-grid">
          <div class="latest-panel">
            <div class="section-head">
              <div>
                <strong>最新值</strong>
                <span v-if="seriesTotal > MAX_DISPLAY_SERIES" class="cardinality-note">已限制 {{ MAX_DISPLAY_SERIES }} 条，匹配总数 {{ seriesTotal }}</span>
              </div>
              <a-tag v-if="staleCount" color="orange">{{ staleCount }} 个实例陈旧</a-tag>
            </div>
            <a-table row-key="series_id" size="small" :bordered="{ cell: true }" :loading="latestLoading" :data="latestRows" :pagination="false" :scroll="{ x: 'max-content', y: 430 }">
              <template #columns>
                <a-table-column title="指标" data-index="metric_name" :width="240" />
                <a-table-column title="实例" data-index="instance_id" :width="180" />
                <a-table-column title="标签" :width="270" :ellipsis="true" :tooltip="true">
                  <template #cell="{ record }">{{ record.labels_json || '-' }}</template>
                </a-table-column>
                <a-table-column title="最新值" :width="130">
                  <template #cell="{ record }">{{ formatNumber(record.value) }}</template>
                </a-table-column>
                <a-table-column title="状态" :width="100">
                  <template #cell="{ record }"><a-tag size="small" :color="statusColor(record)">{{ statusText(record) }}</a-tag></template>
                </a-table-column>
                <a-table-column title="观测时间" :width="190">
                  <template #cell="{ record }">{{ formatTime(record.observed_at) }}</template>
                </a-table-column>
              </template>
            </a-table>
            <div v-if="!latestLoading && !latestRows.length" class="inline-empty">没有匹配的指标序列</div>
          </div>

          <div class="chart-panel">
            <div class="section-head">
              <div>
                <strong>历史趋势</strong>
                <span class="section-meta">近 1 小时 · 最多 {{ MAX_CHART_SERIES }} 条序列</span>
              </div>
              <a-tag v-if="chartPartial" color="orange">部分序列失败</a-tag>
            </div>
            <metric-chart :series="chartPoints" :loading="historyLoading" :error="historyError" @retry="loadHistory" />
          </div>
        </section>
      </a-tab-pane>

      <a-tab-pane key="rules" title="告警规则">
        <section class="rules-panel">
          <div class="section-head">
            <div><strong>指标告警规则</strong><span class="section-meta">平面 A-H 条件，仅支持单层 AND / OR</span></div>
            <a-button type="primary" size="small" @click="openCreateRule">新增规则</a-button>
          </div>
          <a-table row-key="rule_id" size="small" :loading="rulesLoading" :data="rules" :pagination="false" :scroll="{ x: 'max-content' }">
            <template #columns>
              <a-table-column title="名称" :width="220">
                <template #cell="{ record }"><div class="rule-name"><strong>{{ record.name }}</strong><span>{{ record.rule_id }}</span></div></template>
              </a-table-column>
              <a-table-column title="条件" :width="90"><template #cell="{ record }">{{ record.conditions?.length || 0 }} 条</template></a-table-column>
              <a-table-column title="连接" :width="90"><template #cell="{ record }">{{ connectorText(record.connector) }}</template></a-table-column>
              <a-table-column title="状态" :width="150">
                <template #cell="{ record }"><a-space><a-tag size="small" :color="record.enabled ? 'green' : 'gray'">{{ record.enabled ? '启用' : '停用' }}</a-tag><a-tag v-if="ruleState(record.rule_id)?.status" size="small" :color="ruleStatusColor(ruleState(record.rule_id)?.status)">{{ ruleStatusText(ruleState(record.rule_id)?.status) }}</a-tag></a-space></template>
              </a-table-column>
              <a-table-column title="连续计数" :width="110"><template #cell="{ record }">{{ ruleState(record.rule_id)?.trigger_count || 0 }} / {{ record.consecutive_trigger_count }}</template></a-table-column>
              <a-table-column title="最近评估" :width="190"><template #cell="{ record }">{{ formatTime(ruleState(record.rule_id)?.last_evaluated_at) }}</template></a-table-column>
              <a-table-column title="操作" :width="230" align="center">
                <template #cell="{ record }"><a-space><a-button size="mini" type="text" @click="openEditRule(record)">编辑</a-button><a-button size="mini" type="text" @click="openEvaluations(record)">评估记录</a-button><a-popconfirm content="确认删除该规则？" @ok="removeRule(record)"><a-button size="mini" type="text" status="danger">删除</a-button></a-popconfirm></a-space></template>
              </a-table-column>
            </template>
          </a-table>
          <div v-if="!rulesLoading && !rules.length" class="inline-empty">暂无指标告警规则</div>
        </section>
      </a-tab-pane>
    </a-tabs>

    <a-drawer v-model:visible="ruleDrawerVisible" width="1060px" :title="editingRule?.rule_id ? '编辑指标告警规则' : '新建指标告警规则'" :footer="false" unmount-on-close>
      <metric-rule-editor :rule="editingRule" :webhooks="webhooks" @saved="onRuleSaved" @cancel="ruleDrawerVisible = false" />
    </a-drawer>

    <a-drawer v-model:visible="evaluationDrawerVisible" width="900px" title="评估记录" :footer="false" unmount-on-close>
      <a-table size="small" :loading="evaluationLoading" :data="evaluations" :pagination="false" :scroll="{ x: 'max-content' }">
        <template #columns>
          <a-table-column title="时间" :width="190"><template #cell="{ record }">{{ formatTime(record.evaluated_at) }}</template></a-table-column>
          <a-table-column title="状态" :width="110"><template #cell="{ record }"><a-tag :color="ruleStatusColor(record.status)">{{ ruleStatusText(record.status) }}</a-tag></template></a-table-column>
          <a-table-column title="结果" :width="90"><template #cell="{ record }">{{ record.result ? '触发' : '正常' }}</template></a-table-column>
          <a-table-column title="条件结果" :width="440"><template #cell="{ record }">{{ evaluationSummary(record) }}</template></a-table-column>
        </template>
      </a-table>
      <div v-if="!evaluationLoading && !evaluations.length" class="inline-empty">暂无评估记录</div>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import MetricChart, { type ChartPoint } from './metric-chart.vue';
import MetricRuleEditor from './metric-rule-editor.vue';
import { metricMonitorApi } from '@/api/metric-monitor';
import type { MetricHistoryPoint, MetricLatestPoint, MetricNameInfo, MetricRule, MetricRuleEvaluation, MetricSeriesInfo, MetricServiceInfo, MetricRuleState, WebhookChannel } from '@/api/metric-monitor/types';

const props = defineProps<{ embedded?: boolean }>();
const embedded = computed(() => props.embedded === true);

const MAX_DISPLAY_SERIES = 50;
const MAX_CHART_SERIES = 10;
const activeTab = ref('explorer');
const loading = ref(false);
const catalogLoading = ref(false);
const latestLoading = ref(false);
const historyLoading = ref(false);
const rulesLoading = ref(false);
const evaluationLoading = ref(false);
const errorMessage = ref('');
const historyError = ref('');
const chartPartial = ref(false);
const partialState = ref(false);
const services = ref<MetricServiceInfo[]>([]);
const metricOptions = ref<MetricNameInfo[]>([]);
const series = ref<MetricSeriesInfo[]>([]);
const latestRows = ref<MetricLatestPoint[]>([]);
const historyBySeries = ref<Record<string, MetricHistoryPoint[]>>({});
const rules = ref<MetricRule[]>([]);
const ruleStates = ref<Record<string, MetricRuleState>>({});
const webhooks = ref<WebhookChannel[]>([]);
const evaluations = ref<MetricRuleEvaluation[]>([]);
const selectedService = ref('');
const selectedInstance = ref('');
const selectedMetric = ref('');
const labelsFilter = ref('');
const ruleDrawerVisible = ref(false);
const evaluationDrawerVisible = ref(false);
const editingRule = ref<MetricRule>();

const serviceOptions = computed(() => [...new Set(services.value.map((item) => item.service_name).filter(Boolean) as string[])].sort());
const instanceOptions = computed(() => [...new Set(services.value.filter((item) => !selectedService.value || item.service_name === selectedService.value).map((item) => item.instance_id).filter(Boolean) as string[])].sort());
const selectedSeries = computed(() => series.value.filter((item) => (!selectedInstance.value || item.instance_id === selectedInstance.value) && (!labelsFilter.value || (item.labels_json || '').toLowerCase().includes(labelsFilter.value.toLowerCase()))).slice(0, MAX_DISPLAY_SERIES));
const seriesTotal = computed(() => series.value.filter((item) => (!selectedInstance.value || item.instance_id === selectedInstance.value) && (!labelsFilter.value || (item.labels_json || '').toLowerCase().includes(labelsFilter.value.toLowerCase()))).length);
const staleCount = computed(() => latestRows.value.filter((item) => isStale(item)).length);
const chartPoints = computed<ChartPoint[]>(() => Object.entries(historyBySeries.value).flatMap(([seriesId, points]) => points.map((point) => ({ time: formatTime(point.observed_at), value: point.value || 0, series: seriesId.slice(0, 12) }))));

function normalizeRule(rule?: MetricRule): MetricRule {
  return JSON.parse(JSON.stringify(rule || { name: '', conditions: [], connector: 1, consecutive_trigger_count: 3, consecutive_recovery_count: 1, evaluation_interval_seconds: 30, webhook_ids: [], enabled: true })) as MetricRule;
}

async function loadCatalog() {
  catalogLoading.value = true;
  try {
    const response = await metricMonitorApi.listMetricServices({ page: { page: 1, size: 200 } });
    services.value = response.services || [];
    if (!selectedService.value || !serviceOptions.value.includes(selectedService.value)) selectedService.value = serviceOptions.value[0] || '';
    await loadNames();
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '服务指标目录加载失败';
  } finally {
    catalogLoading.value = false;
  }
}

async function loadNames() {
  metricOptions.value = [];
  if (!selectedService.value) {
    selectedMetric.value = '';
    series.value = [];
    return;
  }
  const response = await metricMonitorApi.listMetricNames({ service_name: selectedService.value, page: { page: 1, size: 200 } });
  metricOptions.value = response.names || [];
  if (!selectedMetric.value || !metricOptions.value.some((item) => item.metric_name === selectedMetric.value)) selectedMetric.value = metricOptions.value[0]?.metric_name || '';
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
    const response = await metricMonitorApi.listMetricSeries({ service_name: selectedService.value, metric_name: selectedMetric.value, page: { page: 1, size: MAX_DISPLAY_SERIES } });
    series.value = response.series || [];
    await refreshSeriesData();
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '指标序列加载失败';
  }
}

async function refreshSeriesData() {
  partialState.value = false;
  await Promise.all([loadLatest(), loadHistory()]);
}

async function loadLatest() {
  latestLoading.value = true;
  try {
    const responses = await Promise.allSettled(selectedSeries.value.filter((item) => item.series_id).map((item) => metricMonitorApi.getMetricLatest({ series_id: item.series_id as string })));
    latestRows.value = responses.filter((response): response is PromiseFulfilledResult<{ latest?: MetricLatestPoint }> => response.status === 'fulfilled' && !!response.value.latest).map((response) => response.value.latest as MetricLatestPoint);
    partialState.value = responses.some((response) => response.status === 'rejected');
  } finally {
    latestLoading.value = false;
  }
}

async function loadHistory() {
  historyLoading.value = true;
  historyError.value = '';
  chartPartial.value = false;
  const startAt = new Date(Date.now() - 60 * 60 * 1000).toISOString();
  try {
    const responses = await Promise.allSettled(selectedSeries.value.slice(0, MAX_CHART_SERIES).filter((item) => item.series_id).map((item) => metricMonitorApi.queryMetricHistory({ series_id: item.series_id as string, start_at: startAt, order: 1, limit: 500 })));
    const next: Record<string, MetricHistoryPoint[]> = {};
    responses.forEach((response, index) => {
      const id = selectedSeries.value[index]?.series_id;
      if (response.status === 'fulfilled' && id) next[id] = response.value.points || [];
    });
    historyBySeries.value = next;
    chartPartial.value = responses.some((response) => response.status === 'rejected');
  } catch (error) {
    historyError.value = error instanceof Error ? error.message : '历史数据加载失败';
  } finally {
    historyLoading.value = false;
  }
}

async function loadRules() {
  rulesLoading.value = true;
  try {
    const response = await metricMonitorApi.listMetricRules({ page: { page: 1, size: 100 } });
    rules.value = response.rules || [];
    const states = await Promise.allSettled(rules.value.filter((rule) => rule.rule_id).map((rule) => metricMonitorApi.getMetricRuleState({ rule_id: rule.rule_id as string })));
    ruleStates.value = Object.fromEntries(states.map((response, index) => [rules.value[index]?.rule_id, response.status === 'fulfilled' ? response.value.state : undefined]).filter((entry): entry is [string, MetricRuleState] => !!entry[0] && !!entry[1]));
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '告警规则加载失败';
  } finally {
    rulesLoading.value = false;
  }
}

async function loadWebhooks() {
  try {
    webhooks.value = (await metricMonitorApi.listWebhookChannels()).channels || [];
  } catch {
    webhooks.value = [];
  }
}

async function refreshAll() {
  loading.value = true;
  try {
    await Promise.all([loadCatalog(), loadRules(), loadWebhooks()]);
    Message.success('指标数据已刷新');
  } finally {
    loading.value = false;
  }
}

async function onServiceChange() {
  selectedInstance.value = '';
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
    evaluations.value = (await metricMonitorApi.listMetricRuleEvaluations({ rule_id: rule.rule_id, page: { page: 1, size: 50 } })).evaluations || [];
  } finally {
    evaluationLoading.value = false;
  }
}

function ruleState(ruleId?: string) { return ruleId ? ruleStates.value[ruleId] : undefined; }
function formatTime(value?: string) { if (!value) return '-'; const date = new Date(value); return Number.isNaN(date.valueOf()) ? value : date.toLocaleString(); }
function formatNumber(value?: number) { return value === undefined || value === null ? '-' : Number(value).toLocaleString(undefined, { maximumFractionDigits: 6 }); }
function isStale(row: MetricLatestPoint) { return !!row.stale || (!!row.observed_at && Date.now() - new Date(row.observed_at).valueOf() > 90_000); }
function statusText(row: MetricLatestPoint) { return isStale(row) ? '陈旧' : '正常'; }
function statusColor(row: MetricLatestPoint) { return isStale(row) ? 'orange' : 'green'; }
function connectorText(value?: number) { return value === 2 ? 'OR' : 'AND'; }
function ruleStatusText(value?: number | string) { return value === 2 || value === 'ALERT_STATUS_FIRING' ? 'FIRING' : value === 3 || value === 'ALERT_STATUS_RESOLVED' ? 'RESOLVED' : 'OK'; }
function ruleStatusColor(value?: number | string) { return ruleStatusText(value) === 'FIRING' ? 'red' : ruleStatusText(value) === 'RESOLVED' ? 'blue' : 'green'; }
function evaluationSummary(evaluation: MetricRuleEvaluation) { return (evaluation.conditions || []).map((item) => `${item.condition_id || '-'}:${item.has_data === false ? '无数据' : item.result ? '触发' : '正常'}(${formatNumber(item.value)})`).join(' · '); }

watch(selectedInstance, refreshSeriesData);
onMounted(refreshAll);
</script>

<style scoped lang="scss">
.metric-monitor-page { height: 100%; overflow-y: auto; padding: 0 0 20px; color: var(--color-text-1); }
.page-head, .section-head, .filter-band { display: flex; align-items: center; justify-content: space-between; gap: 14px; }
.page-head { margin-bottom: 16px; }
.page-head h2 { margin: 0 0 6px; font-size: 22px; }
.page-head span, .section-meta, .cardinality-note { color: var(--color-text-3); font-size: 12px; }
.state-alert { margin-bottom: 12px; }
.filter-band { justify-content: flex-start; flex-wrap: wrap; padding: 14px 0; border-bottom: 1px solid var(--color-border-2); }
.option-count { color: var(--color-text-3); }
.state-band, .latest-panel, .chart-panel, .rules-panel { margin-top: 16px; padding: 16px; border: 1px solid var(--color-border-2); background: var(--color-bg-2); }
.state-band { min-height: 220px; display: grid; place-items: center; }
.explorer-grid { display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; }
.latest-panel, .chart-panel { min-width: 0; }
.section-head { margin-bottom: 12px; }
.section-head > div { display: flex; align-items: baseline; gap: 10px; }
.cardinality-note { color: var(--color-warning-6); }
.inline-empty { padding: 28px; text-align: center; color: var(--color-text-3); }
.rule-name { display: flex; flex-direction: column; gap: 3px; }
.rule-name span { color: var(--color-text-3); font-size: 12px; }
</style>

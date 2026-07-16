<template>
  <div class="rule-editor">
    <a-form layout="vertical">
      <div class="form-grid">
        <a-form-item label="规则名称" required><a-input v-model="draft.name" placeholder="例如：订单接口延迟" /></a-form-item>
        <a-form-item label="状态"><a-switch v-model="draft.enabled"><template #checked>启用</template><template #unchecked>停用</template></a-switch></a-form-item>
        <a-form-item label="条件连接"><a-radio-group v-model="draft.connector" type="button"><a-radio :value="LogicalOperator.AND">AND</a-radio><a-radio :value="LogicalOperator.OR">OR</a-radio></a-radio-group></a-form-item>
        <a-form-item label="评估间隔（秒）"><a-input-number v-model="draft.evaluation_interval_seconds" :min="1" :max="86400" /></a-form-item>
        <a-form-item label="连续触发次数"><a-input-number v-model="draft.consecutive_trigger_count" :min="1" :max="100" /></a-form-item>
        <a-form-item label="连续恢复次数"><a-input-number v-model="draft.consecutive_recovery_count" :min="1" :max="100" /></a-form-item>
        <a-form-item class="wide" label="通知 Webhook"><a-select v-model="draft.webhook_ids" multiple allow-clear placeholder="选择通知通道"><a-option v-for="webhook in enabledWebhooks" :key="webhook.webhook_id" :value="webhook.webhook_id">{{ webhook.name || webhook.webhook_id }}</a-option></a-select></a-form-item>
        <a-form-item class="wide" label="说明"><a-textarea v-model="draft.description" :auto-size="{ minRows: 2, maxRows: 4 }" /></a-form-item>
      </div>
    </a-form>

    <div class="conditions-head">
      <div><strong>检测条件</strong><span>最多 8 条，使用同一个 AND / OR 连接符</span></div>
      <a-button size="small" type="primary" status="success" :disabled="draft.conditions.length >= MAX_CONDITIONS" @click="addCondition"><icon-plus /> 添加条件</a-button>
    </div>
    <div v-if="!draft.conditions.length" class="condition-empty">请添加至少一个检测条件。</div>
    <metric-condition-row
      v-for="(condition, index) in draft.conditions"
      :key="condition.condition_id"
      :condition="condition"
      :service-options="serviceOptions"
      :metric-options="metricOptionsFor(condition.query.selector.service_name)"
      :removable="draft.conditions.length > 1"
      @patch="patchCondition(index, $event)"
      @remove="removeCondition(index)"
      @service-change="loadMetricNames"
    />

    <a-divider />
    <div class="preview-head"><strong>保存前预览</strong><a-button size="small" :loading="previewLoading" @click="previewRule">运行预览</a-button></div>
    <a-alert v-if="previewError" type="error" class="preview-alert">{{ previewError }}</a-alert>
    <a-table v-if="preview" size="small" :data="preview.conditions || []" :pagination="false" class="preview-table">
      <template #columns>
        <a-table-column title="条件" data-index="condition_id" :width="90" />
        <a-table-column title="序列数" data-index="selected_series_count" :width="90" />
        <a-table-column title="结果" :width="100"><template #cell="{ record }"><a-tag :color="record.has_data === false ? 'orange' : record.result ? 'red' : 'green'">{{ record.has_data === false ? '无数据' : record.result ? '触发' : '正常' }}</a-tag></template></a-table-column>
        <a-table-column title="值 / 阈值" :width="150"><template #cell="{ record }">{{ formatNumber(record.value) }} / {{ formatNumber(record.threshold) }}</template></a-table-column>
        <a-table-column title="说明" data-index="no_data_reason" />
      </template>
    </a-table>
    <div v-if="preview" class="preview-result">最终结果：<a-tag :color="preview.result ? 'red' : 'green'">{{ preview.result ? '触发' : '正常' }}</a-tag></div>

    <div class="editor-actions"><a-button @click="$emit('cancel')">取消</a-button><a-button type="primary" :loading="saving" @click="saveRule">保存规则</a-button></div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { metricMonitorApi } from '@/api/metric-monitor';
import { LogicalOperator, NoDataPolicy, SeriesReducer, TimeReducer, CompareOperator, type MetricCondition, type MetricNameInfo, type MetricRule, type MetricRuleEvaluation, type MetricConditionPatch, type WebhookChannel } from '@/api/metric-monitor/types';
import MetricConditionRow from './metric-condition-row.vue';

const MAX_CONDITIONS = 8;
const props = defineProps<{ rule?: MetricRule; webhooks: WebhookChannel[] }>();
const emit = defineEmits<{ saved: []; cancel: [] }>();
const draft = ref<MetricRule>(newRule());
const serviceOptions = ref<string[]>([]);
const metricNames = ref<Record<string, MetricNameInfo[]>>({});
const preview = ref<MetricRuleEvaluation>();
const previewLoading = ref(false);
const previewError = ref('');
const saving = ref(false);
const enabledWebhooks = computed(() => props.webhooks.filter((item) => item.enabled !== false));

function newRule(): MetricRule {
  return { name: '', conditions: [newCondition('A')], connector: LogicalOperator.AND, consecutive_trigger_count: 3, consecutive_recovery_count: 1, evaluation_interval_seconds: 30, webhook_ids: [], enabled: true };
}
function newCondition(id: string): MetricCondition {
  return { condition_id: id, query: { selector: { service_name: '', metric_name: '', matchers: [] }, time_reducer: TimeReducer.CURRENT, window_seconds: 0, series_reducer: SeriesReducer.MAX }, compare: CompareOperator.GT, threshold: 0, no_data_policy: NoDataPolicy.KEEP_STATE };
}
function cloneRule(rule?: MetricRule) { return JSON.parse(JSON.stringify(rule || newRule())) as MetricRule; }
function renumber() { draft.value.conditions = draft.value.conditions.slice(0, MAX_CONDITIONS).map((condition, index) => ({ ...condition, condition_id: String.fromCharCode(65 + index) })); }
function addCondition() { if (draft.value.conditions.length < MAX_CONDITIONS) draft.value.conditions.push(newCondition(String.fromCharCode(65 + draft.value.conditions.length))); }
function removeCondition(index: number) { draft.value.conditions.splice(index, 1); renumber(); }
function patchCondition(index: number, patch: MetricConditionPatch) {
  const current = draft.value.conditions[index];
  if (!current) return;
  const query = patch.query || {};
  const selector = query.selector || {};
  draft.value.conditions[index] = { ...current, ...patch, query: { ...current.query, ...query, selector: { ...current.query.selector, ...selector } } } as MetricCondition;
  renumber();
}
function metricOptionsFor(service: string) { return metricNames.value[service] || []; }
async function loadServices() {
  try {
    const response = await metricMonitorApi.listMetricServices({ page: { page: 1, size: 200 } });
    serviceOptions.value = [...new Set((response.services || []).map((item) => item.service_name).filter(Boolean) as string[])].sort();
    await Promise.all(draft.value.conditions.map((condition) => loadMetricNames(condition.query.selector.service_name)));
  } catch {
    serviceOptions.value = [];
  }
}
async function loadMetricNames(service: string) {
  if (!service || metricNames.value[service]) return;
  try {
    const response = await metricMonitorApi.listMetricNames({ service_name: service, page: { page: 1, size: 200 } });
    metricNames.value[service] = response.names || [];
  } catch {
    metricNames.value[service] = [];
  }
}
function validateRule() {
  if (!draft.value.name.trim()) return '规则名称不能为空';
  if (draft.value.conditions.length < 1 || draft.value.conditions.length > MAX_CONDITIONS) return '条件数量必须在 1-8 条之间';
  if (draft.value.webhook_ids.length < 1) return '至少选择一个通知 Webhook';
  if (draft.value.consecutive_trigger_count < 1 || draft.value.consecutive_recovery_count < 1) return '连续触发和恢复次数必须为正数';
  if (draft.value.evaluation_interval_seconds < 1) return '评估间隔必须为正数';
  for (const condition of draft.value.conditions) {
    if (!condition.query.selector.service_name || !condition.query.selector.metric_name) return `条件 ${condition.condition_id} 的服务和指标不能为空`;
    if (condition.query.time_reducer !== TimeReducer.CURRENT && (condition.query.window_seconds || 0) < 1) return `条件 ${condition.condition_id} 的时间窗口必须为正数`;
  }
  return '';
}
async function previewRule() {
  const error = validateRule();
  if (error) { previewError.value = error; return; }
  previewLoading.value = true;
  previewError.value = '';
  try { preview.value = (await metricMonitorApi.previewMetricRule(draft.value)).evaluation; } catch (reason) { previewError.value = reason instanceof Error ? reason.message : '预览失败'; } finally { previewLoading.value = false; }
}
async function saveRule() {
  const error = validateRule();
  if (error) { Message.error(error); return; }
  saving.value = true;
  try {
    if (draft.value.rule_id) await metricMonitorApi.updateMetricRule(draft.value);
    else await metricMonitorApi.createMetricRule(draft.value);
    Message.success('规则已保存');
    emit('saved');
  } catch (reason) { Message.error(reason instanceof Error ? reason.message : '规则保存失败'); } finally { saving.value = false; }
}
function formatNumber(value?: number) { return value === undefined || value === null ? '-' : Number(value).toLocaleString(undefined, { maximumFractionDigits: 6 }); }

watch(() => props.rule, (value) => { draft.value = cloneRule(value); renumber(); preview.value = undefined; previewError.value = ''; void loadServices(); }, { immediate: true });
onMounted(loadServices);
</script>

<style scoped lang="scss">
.rule-editor { padding-bottom: 24px; }
.form-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0 16px; }
.form-grid .wide { grid-column: 1 / -1; }
.conditions-head, .preview-head, .editor-actions { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.conditions-head { border-top: 1px solid var(--color-border-2); padding-top: 16px; }
.conditions-head span { margin-left: 10px; color: var(--color-text-3); font-size: 12px; }
.condition-empty { padding: 18px; color: var(--color-text-3); text-align: center; border-left: 2px solid var(--color-border-2); }
.preview-result { padding: 12px 0; }
.preview-alert { margin: 10px 0; }
.editor-actions { justify-content: flex-end; border-top: 1px solid var(--color-border-2); padding-top: 16px; margin-top: 18px; }
@media (max-width: 900px) { .form-grid { grid-template-columns: 1fr; } .form-grid .wide { grid-column: auto; } }
</style>

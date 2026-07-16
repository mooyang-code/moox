<template>
  <div class="condition-row">
    <div class="condition-index">{{ condition.condition_id }}</div>
    <div class="condition-fields">
      <div class="selector-line">
        <a-select :model-value="condition.query.selector.service_name" placeholder="服务" allow-clear @change="onServiceChange" style="width: 180px">
          <a-option v-for="service in serviceOptions" :key="service" :value="service">{{ service }}</a-option>
        </a-select>
        <a-select :model-value="condition.query.selector.metric_name" placeholder="指标" allow-clear @change="patch({ query: { selector: { metric_name: String($event || '') } } })" style="width: 250px">
          <a-option v-for="metric in metricOptions" :key="metric" :value="metric">{{ metric }}</a-option>
        </a-select>
        <a-button size="small" type="text" status="danger" :disabled="!removable" @click="$emit('remove')"><icon-delete /></a-button>
      </div>
      <div class="matcher-list">
        <div v-for="(matcher, matcherIndex) in condition.query.selector.matchers || []" :key="`${condition.condition_id}-${matcherIndex}`" class="matcher-line">
          <a-select :model-value="matcher.negate" size="small" style="width: 90px" @update:model-value="updateMatcher(matcherIndex, { negate: Boolean($event) })"><a-option :value="false">等于</a-option><a-option :value="true">不等于</a-option></a-select>
          <a-input :model-value="matcher.name" size="small" placeholder="标签名" style="width: 150px" @update:model-value="updateMatcher(matcherIndex, { name: String($event) })" />
          <a-input :model-value="matcher.value" size="small" placeholder="标签值" style="width: 210px" @update:model-value="updateMatcher(matcherIndex, { value: String($event) })" />
          <a-button size="small" type="text" status="danger" @click="removeMatcher(matcherIndex)"><icon-delete /></a-button>
        </div>
        <a-button size="small" type="text" @click="addMatcher"><icon-plus /> 标签条件</a-button>
      </div>
      <div class="reduce-line">
        <a-select :model-value="condition.query.time_reducer" style="width: 130px" @update:model-value="patch({ query: { time_reducer: Number($event) } })"><a-option v-for="item in timeReducerOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option></a-select>
        <a-input-number :model-value="condition.query.window_seconds" :min="condition.query.time_reducer === TimeReducer.CURRENT ? 0 : 1" :max="86400" style="width: 145px" @update:model-value="patch({ query: { window_seconds: Number($event || 0) } })" />
        <span class="unit">秒窗口</span>
        <a-select :model-value="condition.query.series_reducer" style="width: 130px" @update:model-value="patch({ query: { series_reducer: Number($event) } })"><a-option v-for="item in seriesReducerOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option></a-select>
        <a-select :model-value="condition.compare" style="width: 100px" @update:model-value="patch({ compare: Number($event) })"><a-option v-for="item in compareOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option></a-select>
        <a-input-number :model-value="condition.threshold" :precision="6" style="width: 140px" placeholder="阈值" @update:model-value="patch({ threshold: Number($event || 0) })" />
        <a-select :model-value="condition.no_data_policy" style="width: 130px" @update:model-value="patch({ no_data_policy: Number($event) })"><a-option v-for="item in noDataOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option></a-select>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { CompareOperator, NoDataPolicy, SeriesReducer, TimeReducer, type MetricCondition, type MetricNameInfo, type MetricConditionPatch } from '@/api/metric-monitor/types';

const props = defineProps<{ condition: MetricCondition; serviceOptions: string[]; metricOptions: MetricNameInfo[]; removable: boolean }>();
const emit = defineEmits<{ patch: [value: MetricConditionPatch]; remove: []; serviceChange: [service: string] }>();

const timeReducerOptions = [
  { value: TimeReducer.CURRENT, label: '当前值' },
  { value: TimeReducer.AVG, label: '平均值' },
  { value: TimeReducer.MIN, label: '最小值' },
  { value: TimeReducer.MAX, label: '最大值' },
  { value: TimeReducer.SUM, label: '求和' },
  { value: TimeReducer.RATE, label: '速率' },
  { value: TimeReducer.INCREASE, label: '增量' },
];
const seriesReducerOptions = [
  { value: SeriesReducer.AVG, label: '序列平均' },
  { value: SeriesReducer.MIN, label: '序列最小' },
  { value: SeriesReducer.MAX, label: '序列最大' },
  { value: SeriesReducer.SUM, label: '序列求和' },
];
const compareOptions = [
  { value: CompareOperator.GT, label: '>' },
  { value: CompareOperator.GTE, label: '>=' },
  { value: CompareOperator.LT, label: '<' },
  { value: CompareOperator.LTE, label: '<=' },
  { value: CompareOperator.EQ, label: '=' },
  { value: CompareOperator.NEQ, label: '!=' },
];
const noDataOptions = [
  { value: NoDataPolicy.KEEP_STATE, label: '无数据保持' },
  { value: NoDataPolicy.OK, label: '无数据正常' },
  { value: NoDataPolicy.FIRING, label: '无数据触发' },
];

const metricOptions = computed(() => props.metricOptions.map((item) => item.metric_name || '').filter(Boolean));
function patch(value: MetricConditionPatch) { emit('patch', value); }
function onServiceChange(value: string) { patch({ query: { selector: { service_name: value, metric_name: '' } } }); emit('serviceChange', value); }
function addMatcher() { patch({ query: { selector: { matchers: [...(props.condition.query.selector.matchers || []), { name: '', value: '', negate: false }] } } }); }
function removeMatcher(index: number) { patch({ query: { selector: { matchers: (props.condition.query.selector.matchers || []).filter((_, matcherIndex) => matcherIndex !== index) } } }); }
function updateMatcher(index: number, value: Partial<{ name: string; value: string; negate: boolean }>) {
  patch({ query: { selector: { matchers: (props.condition.query.selector.matchers || []).map((matcher, matcherIndex) => matcherIndex === index ? { ...matcher, ...value } : matcher) } } });
}
</script>

<style scoped lang="scss">
.condition-row { display: flex; gap: 10px; border-left: 2px solid var(--color-primary-light-3); padding: var(--moox-space-3) 0 var(--moox-space-3) var(--moox-space-3); }
.condition-index { width: 28px; height: 28px; display: grid; place-items: center; flex: 0 0 auto; border: 1px solid var(--color-primary-light-3); color: var(--color-primary-6); font-weight: 600; }
.condition-fields { flex: 1; min-width: 0; display: grid; gap: var(--moox-space-2); }
.selector-line, .matcher-line, .reduce-line { display: flex; align-items: center; flex-wrap: wrap; gap: var(--moox-space-2); }
.matcher-list { display: grid; gap: 7px; padding-left: var(--moox-space-1); }
.reduce-line { border-top: 1px solid var(--color-border-2); padding-top: var(--moox-space-2); }
.unit { color: var(--color-text-3); font-size: 12px; }
</style>

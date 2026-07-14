import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, '..');
const read = (file) => fs.readFileSync(path.join(root, file), 'utf8');
const route = read('src/router/route.ts');
const menu = read('src/api/modules/system/static-menu.ts');
const api = read('src/api/metric-monitor/index.ts');
const types = read('src/api/metric-monitor/types.ts');
const dashboard = read('src/views/ops/metric-monitor/index.vue');
const chart = read('src/views/ops/metric-monitor/metric-chart.vue');
const editor = read('src/views/ops/metric-monitor/metric-rule-editor.vue');
const condition = read('src/views/ops/metric-monitor/metric-condition-row.vue');

function assert(conditionValue, message) {
  if (!conditionValue) throw new Error(message);
}

assert(route.includes('path: "/ops/metric-monitor"'), 'metric monitor legacy route is missing');
assert(menu.includes('"ops-services"') && !menu.includes('menu("0607"'), 'metric monitor must be grouped under service management');
assert(api.includes("'ListMetricServices'") && api.includes("'ListMetricNames'") && api.includes("'ListMetricSeries'"), 'metric catalog RPC methods are missing');
assert(api.includes("'GetMetricLatest'") && api.includes("'QueryMetricHistory'"), 'metric query RPC methods are missing');
for (const method of ['ListMetricRules', 'GetMetricRule', 'CreateMetricRule', 'UpdateMetricRule', 'DeleteMetricRule', 'PreviewMetricRule', 'ListMetricRuleEvaluations', 'GetMetricRuleState']) {
  assert(api.includes(`'${method}'`), `${method} RPC method is missing`);
}
assert(types.includes('METRIC_HISTORY_LIMIT = 500'), 'history limit must be bounded to 500');
assert(api.includes('boundedLimit(req.limit, METRIC_HISTORY_LIMIT'), 'history API must clamp the requested limit');
assert(dashboard.includes('暂无已上报服务') && dashboard.includes('没有匹配的指标序列'), 'empty catalog/no matching series states are missing');
assert(dashboard.includes('陈旧') && dashboard.includes('部分序列暂不可用'), 'stale/partial states are missing');
assert(dashboard.includes('historyError') && dashboard.includes('@retry="loadHistory"'), 'history error retry state is missing');
assert(chart.includes('seriesField: \'series\'') && chart.includes('new VChart'), 'VChart series mapping is missing');
assert(editor.includes('previewMetricRule') && editor.includes('保存前预览'), 'structured rule preview is missing');
assert(condition.includes('标签条件') && condition.includes('timeReducerOptions') && condition.includes('compareOptions') && condition.includes('noDataOptions'), 'condition selectors are incomplete');
assert(editor.includes('LogicalOperator.AND') && editor.includes('LogicalOperator.OR'), 'AND/OR connector control is missing');
assert(editor.includes('MAX_CONDITIONS = 8') && editor.includes('String.fromCharCode(65'), 'flat A-H condition bounds are missing');
assert(editor.includes('consecutive_trigger_count') && editor.includes('consecutive_recovery_count'), 'consecutive trigger/recovery controls are missing');
assert(editor.includes('webhook_ids') && editor.includes('multiple'), 'multi-webhook selector is missing');
assert(!/Create(?:Monitor)?Target|Add(?:Monitor)?Target|scrape_target|target_url/i.test(api + dashboard + editor), 'manual monitoring target API/control must not be present');
assert(!/promql|free.?text|expression/i.test(editor + condition), 'free-text metric DSL must not be present in the rule editor');
assert(dashboard.includes('class="latest-panel"') && dashboard.includes('class="chart-panel"'), 'metric monitor must expose latest and history panels');
assert(dashboard.includes('grid-template-columns: minmax(0, 1fr)'), 'metric monitor must use a full-width detail layout');

console.log('metric monitor dashboard and rule editor contract ok');

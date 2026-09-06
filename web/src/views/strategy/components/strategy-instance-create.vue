<template>
  <a-drawer :visible="visible" :width="'min(760px, 100vw)'" title="新建策略实例" unmount-on-close @cancel="close" @ok="submit">
    <a-steps :current="step" size="small" class="steps"><a-step title="实例" /><a-step title="绑定" /><a-step title="确认" /></a-steps>
    <a-alert v-if="error" type="error" show-icon class="form-alert">{{ error }}</a-alert>
    <a-form v-if="step === 1" layout="vertical">
      <a-form-item label="实例 ID" required><a-input v-model="form.instance_id" placeholder="例如 momentum-paper-1" /></a-form-item>
      <a-form-item label="策略定义" required><a-select v-model="form.strategy_id" allow-search @change="resetBindings"><a-option v-for="item in strategies" :key="item.strategy_id" :value="item.strategy_id">{{ item.name }}（{{ item.strategy_id }}）</a-option></a-select></a-form-item>
      <a-descriptions v-if="selectedStrategy" :column="2" bordered><a-descriptions-item label="名称">{{ selectedStrategy.name }}</a-descriptions-item><a-descriptions-item label="DSL 周期">{{ dslPreview?.bar || "-" }}</a-descriptions-item><a-descriptions-item label="触发方式">{{ dslPreview?.triggers.join("、") || "-" }}</a-descriptions-item><a-descriptions-item label="规则">{{ dslPreview?.rules.join("、") || "-" }}</a-descriptions-item></a-descriptions>
    </a-form>
    <a-form v-else-if="step === 2" layout="vertical">
      <a-form-item label="源 View" required><a-select v-model="form.source_view_id" allow-search :loading="metadataLoading" @change="loadSourceColumns"><a-option v-for="view in views" :key="view.view_id" :value="view.view_id">{{ view.name || view.view_id }}（{{ view.view_id }}）</a-option></a-select></a-form-item>
      <a-form-item label="实例频率" required><a-input v-model="form.frequency" readonly :placeholder="dslPreview?.bar || '与 DSL data.bar 一致'" /></a-form-item><a-alert v-if="sourceFrequencyError" type="warning" show-icon>{{ sourceFrequencyError }}</a-alert>
      <div class="binding-head"><strong>因子绑定（可选）</strong><a-button size="small" type="primary" status="success" @click="addFactor"><template #icon><icon-plus /></template>添加因子</a-button></div>
      <div v-for="(row, index) in factors" :key="row.key" class="binding-row">
        <a-select v-model="row.factor_id" allow-search placeholder="因子定义" @change="factorChanged(row)"><a-option v-for="factor in factorDefs" :key="factor.factor_id" :value="factor.factor_id">{{ factor.name }}（{{ factor.factor_id }}）</a-option></a-select>
        <a-select v-model="row.binding_id" allow-search placeholder="绑定" :disabled="!row.factor_id" @change="bindingChanged(row)"><a-option v-for="binding in bindingsFor(row.factor_id)" :key="binding.binding_id" :value="binding.binding_id">{{ binding.binding_id }} · {{ binding.result_view_id }}</a-option></a-select>
        <a-select v-model="row.output" placeholder="输出" :disabled="!row.factor_id" @change="row.column_name = ''"><a-option v-for="output in factorFor(row.factor_id)?.outputs || []" :key="output" :value="output">{{ output }}</a-option></a-select>
        <a-select v-model="row.column_name" placeholder="结果列" :disabled="!row.output || !row.binding_id"><a-option v-for="column in resultColumns(row)" :key="column.column_name" :value="column.column_name">{{ column.column_name }}</a-option></a-select>
        <a-button type="text" status="danger" aria-label="移除因子" @click="removeFactor(index)"><template #icon><icon-delete /></template></a-button>
      </div>
      <a-alert v-if="bindingReason" type="warning" show-icon>{{ bindingReason }}</a-alert>
      <a-divider />
      <a-form-item label="绑定 JSON（自动生成，只读）"><a-textarea :model-value="bindingsJson" readonly :auto-size="{ minRows: 7, maxRows: 13 }" class="code-input" /></a-form-item>
    </a-form>
    <a-form v-else layout="vertical">
      <a-form-item label="运行模式"><a-radio-group v-model="form.logical_account_id"><a-radio value="">仅计算</a-radio><a-radio v-for="account in accounts" :key="account.logical_account_id" :value="account.logical_account_id">发送给交易模块：{{ account.name || account.logical_account_id }}</a-radio></a-radio-group></a-form-item>
      <a-descriptions :column="1" bordered><a-descriptions-item label="实例 ID">{{ form.instance_id }}</a-descriptions-item><a-descriptions-item label="策略">{{ selectedStrategy?.name || form.strategy_id }}</a-descriptions-item><a-descriptions-item label="输入">{{ form.source_view_id }} · {{ form.frequency }}</a-descriptions-item><a-descriptions-item label="模式"><a-tag :color="form.logical_account_id ? 'orange' : 'blue'">{{ form.logical_account_id ? "发送给交易模块" : "仅计算" }}</a-tag></a-descriptions-item><a-descriptions-item v-if="form.logical_account_id" label="逻辑账户">{{ form.logical_account_id }}</a-descriptions-item></a-descriptions>
      <a-alert type="info" show-icon class="confirm-note">创建后实例保持停用。启用时后台才会完整校验依赖并在交易模式下认领账户会话。</a-alert>
    </a-form>
    <template #footer><a-space><a-button @click="step === 1 ? close() : step--">{{ step === 1 ? "取消" : "上一步" }}</a-button><a-button v-if="step < 3" type="primary" @click="next">下一步</a-button><a-button v-else type="primary" status="success" :loading="saving" @click="submit">创建停用实例</a-button></a-space></template>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { createInstance, getInstance } from "@/api/strategy";
import { listFactorBindings, listFactorDefs } from "@/api/factor";
import type { FactorBinding, FactorDef } from "@/api/factor/types";
import { getDataset, listViewColumns, listViews } from "@/api/storage/metadata";
import type { View, ViewColumn } from "@/api/storage/types";
import { listLogicalAccounts } from "@/api/trade";
import type { LogicalAccount } from "@/api/trade/types";
import { parseDSL, requiredFactorFields } from "@/views/strategy/dsl";
import { buildInputBindings, canCombineSelections, findOutputColumn, normalizeFrequency, validBindings, validateAliasConflicts, type BindingSelection } from "@/views/strategy/bindings";
import { freqFromViewFilterJSON } from "@/views/data/views/view-form-utils";
import { isTimeSeriesDataKind } from "@/views/data/shared/metadata-utils";
import type { Strategy } from "@/api/strategy-types";

interface FactorRow { key: number; factor_id: string; binding_id: string; output: string; column_name: string; }
const props = defineProps<{ visible: boolean; strategies: Strategy[]; spaceId: string }>();
const emit = defineEmits<{ "update:visible": [boolean]; created: [string] }>();
const step = ref(1); const saving = ref(false); const metadataLoading = ref(false); const error = ref(""); const bindingReason = ref(""); const sourceFrequencyError = ref("");
const form = reactive({ instance_id: "", strategy_id: "", source_view_id: "", frequency: "", logical_account_id: "" });
const factors = ref<FactorRow[]>([]); const views = ref<View[]>([]); const factorDefs = ref<FactorDef[]>([]); const factorBindings = ref<FactorBinding[]>([]); const accounts = ref<LogicalAccount[]>([]); const columnsByView = reactive<Record<string, ViewColumn[]>>({});
let nextKey = 1;
let metadataRequest = 0;
let submitRequest = 0;
let sourceRequest = 0;
const selectedStrategy = computed(() => props.strategies.find(item => item.strategy_id === form.strategy_id));
const dslPreview = computed(() => selectedStrategy.value ? parseDSL(selectedStrategy.value.dsl_yaml).preview : null);
const bindingsJson = computed(() => { const source = views.value.find(view => view.view_id === form.source_view_id); if (!source) return "{}"; const selections = factors.value.map(row => { const factor = factorFor(row.factor_id); const binding = bindingFor(row.binding_id); const column = row.column_name; return factor && binding && column ? { factor, binding, output: row.output, column_name: column } : null; }).filter(Boolean) as BindingSelection[]; return buildInputBindings(source, form.frequency || dslPreview.value?.bar || "", selections); });

function close() { submitRequest += 1; saving.value = false; emit("update:visible", false); }
async function loadAllViews(spaceId: string) { const items: View[] = []; for (let page = 1; ; page += 1) { const rsp = await listViews({ space_id: spaceId, status: "active", page: { page, size: 200 } }); items.push(...(rsp.views || [])); if (!rsp.page_result?.has_more || !(rsp.views || []).length) return items; } }
async function loadAllFactors() { const items: FactorDef[] = []; for (let page = 1; ; page += 1) { const rsp = await listFactorDefs({ status: "enabled", page: { page, size: 200 } }); items.push(...(rsp.factors || [])); if (!rsp.page_result?.has_more || !(rsp.factors || []).length) return items; } }
async function loadAllAccounts() { const items: LogicalAccount[] = []; for (let page = 1; ; page += 1) { const rsp = await listLogicalAccounts({ page, size: 200 }); items.push(...(rsp.logical_accounts || [])); if (!rsp.page_result?.has_more || !(rsp.logical_accounts || []).length) return items; } }
async function loadAllBindings(spaceId: string, sourceViewId?: string) { const items: FactorBinding[] = []; for (let page = 1; ; page += 1) { const rsp = await listFactorBindings({ space_id: spaceId, source_view_id: sourceViewId, status: "enabled", page: { page, size: 500 } }); items.push(...(rsp.bindings || [])); if (!rsp.page_result?.has_more || !(rsp.bindings || []).length) return items; } }
async function loadMetadata() { const spaceId = props.spaceId; if (!spaceId) return; const requestId = ++metadataRequest; metadataLoading.value = true; error.value = ""; try { const [nextViews, nextFactors, nextAccounts, nextBindings] = await Promise.all([loadAllViews(spaceId), loadAllFactors(), loadAllAccounts(), loadAllBindings(spaceId)]); if (requestId !== metadataRequest || props.spaceId !== spaceId) return; views.value = nextViews.filter(view => view.space_id === spaceId); factorDefs.value = nextFactors; accounts.value = nextAccounts.filter(account => account.space_id === spaceId); factorBindings.value = nextBindings.filter(binding => binding.space_id === spaceId); } catch (err) { if (requestId === metadataRequest) error.value = err instanceof Error ? err.message : "元数据加载失败"; } finally { if (requestId === metadataRequest) metadataLoading.value = false; } }
async function loadSourceColumns() {
  const requestId = ++sourceRequest;
  const spaceId = props.spaceId;
  const source = views.value.find(view => view.view_id === form.source_view_id && view.space_id === spaceId);
  sourceFrequencyError.value = "";
  if (source) {
    form.frequency = form.frequency || dslPreview.value?.bar || "";
    await ensureColumns(source.view_id, spaceId);
    try {
      const dataset = await getDataset({ space_id: spaceId, dataset_id: source.primary_dataset_id });
      if (requestId !== sourceRequest || props.spaceId !== spaceId) return;
      const supported = dataset.dataset?.freqs || [];
      const viewFrequency = freqFromViewFilterJSON(source.filter_json);
      if (!isTimeSeriesDataKind(dataset.dataset?.data_kind)) sourceFrequencyError.value = "策略源 View 必须基于时序数据集";
      else if (!supported.length || !viewFrequency) sourceFrequencyError.value = "策略源 View 必须声明可用周期和实际 View 周期";
      else if (!supported.some(value => normalizeFrequency(value) === normalizeFrequency(form.frequency))) sourceFrequencyError.value = `源 View 主数据集不支持 ${form.frequency}，可用周期：${supported.join("、")}`;
      else if (normalizeFrequency(viewFrequency) !== normalizeFrequency(form.frequency)) sourceFrequencyError.value = `源 View 实际周期为 ${viewFrequency}，与 DSL data.bar ${form.frequency} 不一致`;
    } catch (err) {
      if (requestId === sourceRequest && props.spaceId === spaceId) sourceFrequencyError.value = err instanceof Error ? `无法读取源 View 周期：${err.message}` : "无法读取源 View 周期";
    }
  }
  if (requestId !== sourceRequest || props.spaceId !== spaceId) return;
  const nextBindings = spaceId ? await loadAllBindings(spaceId, form.source_view_id) : [];
  if (requestId !== sourceRequest || props.spaceId !== spaceId || form.source_view_id !== source?.view_id) return;
  factorBindings.value = nextBindings;
}
async function ensureColumns(viewId: string, spaceId = props.spaceId) {
  const cacheKey = `${spaceId}/${viewId}`;
  if (columnsByView[cacheKey]) return;
  const items: ViewColumn[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listViewColumns({ space_id: spaceId, view_id: viewId, page: { page, size: 500 } });
    items.push(...(rsp.columns || []));
    if (!rsp.page_result?.has_more || !(rsp.columns || []).length) break;
  }
  columnsByView[cacheKey] = items;
}
function factorFor(id: string) { return factorDefs.value.find(item => item.factor_id === id); }
function bindingFor(id: string) { return factorBindings.value.find(item => item.binding_id === id); }
function bindingsFor(factorId: string) { return validBindings(factorBindings.value.filter(item => item.factor_id === factorId), form.source_view_id, form.frequency); }
function resultColumns(row: FactorRow) { const binding = bindingFor(row.binding_id); const factor = factorFor(row.factor_id); if (!binding || !factor) return []; const columns = columnsByView[`${binding.space_id}/${binding.result_view_id || ""}`] || []; const match = factor.outputs.map(output => findOutputColumn(columns, factor.factor_id, output)).filter(Boolean) as ViewColumn[]; return row.output ? match.filter(column => column.attributes?.factor_output === row.output) : match; }
async function factorChanged(row: FactorRow) { row.binding_id = ""; row.output = ""; row.column_name = ""; }
async function bindingChanged(row: FactorRow) { row.output = ""; row.column_name = ""; const binding = bindingFor(row.binding_id); if (binding?.result_view_id) await ensureColumns(binding.result_view_id); }
function addFactor() { factors.value.push({ key: nextKey++, factor_id: "", binding_id: "", output: "", column_name: "" }); }
function removeFactor(index: number) { factors.value.splice(index, 1); }
function resetBindings() { form.source_view_id = ""; form.frequency = ""; factors.value = []; factorBindings.value = []; bindingReason.value = ""; sourceFrequencyError.value = ""; }
function resetForm() { submitRequest += 1; saving.value = false; form.instance_id = ""; form.strategy_id = ""; form.source_view_id = ""; form.frequency = ""; form.logical_account_id = ""; factors.value = []; views.value = []; factorDefs.value = []; factorBindings.value = []; accounts.value = []; Object.keys(columnsByView).forEach(key => delete columnsByView[key]); bindingReason.value = ""; sourceFrequencyError.value = ""; }
function validateStep() {
  error.value = "";
  if (step.value === 1 && (!form.instance_id.trim() || !form.strategy_id)) {
    error.value = "请填写实例 ID 并选择策略定义";
    return false;
  }
  if (step.value !== 2) return true;
  const source = views.value.find(view => view.view_id === form.source_view_id);
  if (!source) { error.value = "请选择源 View"; return false; }
  if (sourceFrequencyError.value) { error.value = sourceFrequencyError.value; return false; }
  if (!form.frequency.trim() || normalizeFrequency(form.frequency) !== normalizeFrequency(dslPreview.value?.bar || "")) {
    error.value = `实例频率必须与 DSL data.bar (${dslPreview.value?.bar || "未配置"}) 一致`;
    return false;
  }
  const selections = factors.value.map(row => {
    const factor = factorFor(row.factor_id);
    const binding = bindingFor(row.binding_id);
    return factor && binding && row.output && row.column_name ? { factor, binding, output: row.output, column_name: row.column_name } : null;
  });
  if (selections.some(value => !value)) { error.value = "每个因子都必须选择定义、绑定、输出和结果列"; return false; }
  const selectedOutputs = new Set(selections.filter(Boolean).flatMap(selection => [selection!.factor.factor_id, selection!.output, selection!.column_name, ...(selection!.factor.input_columns || [])]));
  const missingFields = requiredFactorFields(selectedStrategy.value?.dsl_yaml || "").filter(field => !selectedOutputs.has(field));
  if (missingFields.length) { error.value = `DSL 需要因子输出 ${missingFields.join("、")}，请完成对应绑定`; return false; }
  const aliasError = validateAliasConflicts(selections.filter(Boolean) as BindingSelection[]);
  if (aliasError) { error.value = aliasError; return false; }
  const selectedFactorIds = factors.value.map(row => row.factor_id).filter(Boolean);
  const selectedBindingIds = factors.value.map(row => row.binding_id).filter(Boolean);
  if (new Set(selectedFactorIds).size !== selectedFactorIds.length || new Set(selectedBindingIds).size !== selectedBindingIds.length) {
    error.value = "同一个因子或绑定不能重复添加";
    return false;
  }
  const result = canCombineSelections(selections.filter(Boolean) as BindingSelection[], source);
  if (!result.ok) { bindingReason.value = result.reason || "因子绑定不兼容"; error.value = bindingReason.value; return false; }
  if (selections.length && !["factor.ready", "viewfactorperiodready", "event.storage.view.factor_period.ready"].includes((dslPreview.value?.eventName || "").trim().toLowerCase())) {
    error.value = "绑定因子需要 DSL 配置 factor.ready 事件；source.ready 或纯定时触发不能运行因子策略";
    return false;
  }
  return true;
}
function next() { if (!validateStep()) return; step.value += 1; }
async function submit() { if (!validateStep()) return; const spaceId = props.spaceId; const requestId = ++submitRequest; const source = views.value.find(view => view.view_id === form.source_view_id); const selections = factors.value.map(row => bindingFor(row.binding_id)).filter(Boolean); if (!source || source.space_id !== spaceId || selections.some(binding => binding?.space_id !== spaceId)) { error.value = "空间已切换，请重新选择源 View 和因子绑定"; return; } saving.value = true; error.value = ""; try { const response = await createInstance({ instance_id: form.instance_id.trim(), strategy_id: form.strategy_id, space_id: spaceId, input_bindings_json: bindingsJson.value, logical_account_id: form.logical_account_id }); if (requestId !== submitRequest || props.spaceId !== spaceId) { Message.info("实例已创建，但当前空间已切换；请在原空间实例列表中确认"); return; } emit("created", response.instance.instance_id); close(); Message.success("策略实例已创建并保持停用"); } catch (err) { if (requestId !== submitRequest || props.spaceId !== spaceId) return; try { await getInstance(form.instance_id.trim()); error.value = "创建请求结果未知，但实例 ID 已存在，请返回列表确认，不要重复创建"; } catch { error.value = err instanceof Error ? err.message : "策略实例创建失败"; } } finally { if (requestId === submitRequest) saving.value = false; } }
watch(() => props.visible, value => { if (value) { resetForm(); step.value = 1; error.value = ""; loadMetadata(); } });
watch(() => props.spaceId, (value, previous) => { if (value !== previous && props.visible) { resetForm(); step.value = 1; loadMetadata(); } });
</script>

<style scoped>
.steps { margin-bottom: 22px; }
.form-alert { margin-bottom: 16px; }
.binding-head { display: flex; align-items: center; justify-content: space-between; margin: 10px 0; }
.binding-row { display: grid; grid-template-columns: 1.1fr 1.2fr .8fr 1fr 32px; gap: 8px; align-items: center; margin-bottom: 8px; }
.code-input { font: 12px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace; }
.confirm-note { margin-top: 16px; }
@media (max-width: 700px) { .binding-row { grid-template-columns: 1fr 1fr; } .binding-row .arco-btn { grid-column: 2; justify-self: end; } }
</style>

<template>
  <a-drawer :visible="visible" :width="'min(760px, 100vw)'" title="新建策略实例" unmount-on-close @cancel="close" @ok="submit">
    <a-steps :current="step" size="small" class="steps">
      <a-step title="实例" />
      <a-step title="绑定" />
      <a-step title="确认" />
    </a-steps>
    <a-alert v-if="error" type="error" show-icon class="form-alert">{{ error }}</a-alert>

    <a-form v-if="step === 1" layout="vertical">
      <a-form-item label="实例 ID" required><a-input v-model="form.instance_id" placeholder="例如 momentum-paper-1" /></a-form-item>
      <a-form-item label="策略定义" required>
        <a-select v-model="form.strategy_id" allow-search @change="resetBindings">
          <a-option v-for="item in strategies" :key="item.strategy_id" :value="item.strategy_id">
            {{ item.name }}（{{ item.strategy_id }}）
          </a-option>
        </a-select>
      </a-form-item>
      <a-descriptions v-if="selectedStrategy" :column="2" bordered>
        <a-descriptions-item label="名称">{{ selectedStrategy.name }}</a-descriptions-item>
        <a-descriptions-item label="DSL 周期">{{ dslPreview?.bar || "-" }}</a-descriptions-item>
        <a-descriptions-item label="触发方式">{{ dslPreview?.triggers.join("、") || "-" }}</a-descriptions-item>
        <a-descriptions-item label="规则">{{ dslPreview?.rules.join("、") || "-" }}</a-descriptions-item>
      </a-descriptions>
    </a-form>

    <a-form v-else-if="step === 2" layout="vertical">
      <a-form-item label="源 View" required>
        <a-select v-model="form.source_view_id" allow-search :loading="metadataLoading" @change="loadSourceColumns">
          <a-option v-for="view in views" :key="view.view_id" :value="view.view_id">
            {{ view.name || view.view_id }}（{{ view.view_id }}）
          </a-option>
        </a-select>
      </a-form-item>
      <a-form-item label="实例频率" required>
        <a-input v-model="form.frequency" readonly :placeholder="dslPreview?.bar || '与 DSL data.bar 一致'" />
      </a-form-item>
      <a-alert v-if="sourceFrequencyError" type="warning" show-icon>{{ sourceFrequencyError }}</a-alert>
      <div class="binding-head">
        <strong>因子输出（可选）</strong>
        <a-button size="small" type="primary" status="success" :disabled="!compatibleSets.length" @click="addFactor">
          <template #icon><icon-plus /></template>添加因子
        </a-button>
      </div>
      <div v-for="(row, index) in factors" :key="row.key" class="binding-row">
        <a-select v-model="row.set_id" allow-search placeholder="计算任务" @change="setChanged(row)">
          <a-option v-for="setInfo in compatibleSets" :key="setInfo.factor_set.set_id" :value="setInfo.factor_set.set_id">
            {{ setInfo.factor_set.set_id }} · {{ setInfo.factor_set.freq }}
          </a-option>
        </a-select>
        <a-select v-model="row.factor_id" allow-search placeholder="因子定义" :disabled="!row.set_id" @change="factorChanged(row)">
          <a-option v-for="factor in factorsForSet(row.set_id)" :key="factor.factor_id" :value="factor.factor_id">
            {{ factor.name }}（{{ factor.factor_id }}）
          </a-option>
        </a-select>
        <a-select v-model="row.output" placeholder="输出" :disabled="!row.factor_id" @change="row.column_name = ''">
          <a-option v-for="output in factorFor(row)?.outputs || []" :key="output" :value="output">{{ output }}</a-option>
        </a-select>
        <a-select v-model="row.column_name" placeholder="结果列" :disabled="!row.output">
          <a-option v-for="column in resultColumns(row)" :key="column.column_name" :value="column.column_name">
            {{ column.column_name }}
          </a-option>
        </a-select>
        <a-button type="text" status="danger" aria-label="移除因子" @click="removeFactor(index)"><template #icon><icon-delete /></template></a-button>
      </div>
      <a-alert v-if="bindingReason" type="warning" show-icon>{{ bindingReason }}</a-alert>
      <a-divider />
      <a-form-item label="绑定 JSON（自动生成，只读）">
        <a-textarea :model-value="bindingsJson" readonly :auto-size="{ minRows: 7, maxRows: 13 }" class="code-input" />
      </a-form-item>
    </a-form>

    <a-form v-else layout="vertical">
      <a-form-item label="运行模式">
        <a-radio-group v-model="form.logical_account_id">
          <a-radio value="">仅计算</a-radio>
          <a-radio v-for="account in accounts" :key="account.logical_account_id" :value="account.logical_account_id">
            发送给交易模块：{{ account.name || account.logical_account_id }}
          </a-radio>
        </a-radio-group>
      </a-form-item>
      <a-descriptions :column="1" bordered>
        <a-descriptions-item label="实例 ID">{{ form.instance_id }}</a-descriptions-item>
        <a-descriptions-item label="策略">{{ selectedStrategy?.name || form.strategy_id }}</a-descriptions-item>
        <a-descriptions-item label="输入">{{ form.source_view_id }} · {{ form.frequency }}</a-descriptions-item>
        <a-descriptions-item label="模式"><a-tag :color="form.logical_account_id ? 'orange' : 'blue'">{{ form.logical_account_id ? "发送给交易模块" : "仅计算" }}</a-tag></a-descriptions-item>
        <a-descriptions-item v-if="form.logical_account_id" label="逻辑账户">{{ form.logical_account_id }}</a-descriptions-item>
      </a-descriptions>
      <a-alert type="info" show-icon class="confirm-note">创建后实例保持停用。启用时后台才会完整校验依赖并在交易模式下认领账户会话。</a-alert>
    </a-form>

    <template #footer>
      <a-space>
        <a-button @click="step === 1 ? close() : step--">{{ step === 1 ? "取消" : "上一步" }}</a-button>
        <a-button v-if="step < 3" type="primary" @click="next">下一步</a-button>
        <a-button v-else type="primary" status="success" :loading="saving" @click="submit">创建停用实例</a-button>
      </a-space>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { listFactorSets } from "@/api/factor";
import type { FactorSetInfo } from "@/api/factor/types";
import { createInstance, getInstance } from "@/api/strategy";
import { getDataset, listViewColumns, listViews } from "@/api/storage/metadata";
import type { View, ViewColumn } from "@/api/storage/types";
import { listLogicalAccounts } from "@/api/trade";
import type { LogicalAccount } from "@/api/trade/types";
import { parseDSL, requiredFactorFields } from "@/views/strategy/dsl";
import { buildInputBindings, canCombineSelections, enabledFactors, findOutputColumn, validFactorSets, validateAliasConflicts, type BindingSelection } from "@/views/strategy/bindings";
import { normalizeFrequency } from "@/utils/frequency";
import { isTimeSeriesDataKind } from "@/views/data/shared/metadata-utils";
import type { Strategy } from "@/api/strategy-types";

interface FactorRow { key: number; set_id: string; factor_id: string; output: string; column_name: string; }
interface ResultViewInfo { view: View; columns: ViewColumn[]; }

const props = defineProps<{ visible: boolean; strategies: Strategy[]; spaceId: string }>();
const emit = defineEmits<{ "update:visible": [boolean]; created: [string] }>();
const step = ref(1);
const saving = ref(false);
const metadataLoading = ref(false);
const error = ref("");
const bindingReason = ref("");
const sourceFrequencyError = ref("");
const form = reactive({ instance_id: "", strategy_id: "", source_view_id: "", frequency: "", logical_account_id: "" });
const factors = ref<FactorRow[]>([]);
const views = ref<View[]>([]);
const factorSets = ref<FactorSetInfo[]>([]);
const accounts = ref<LogicalAccount[]>([]);
const resultViews = reactive<Record<string, ResultViewInfo>>({});
let nextKey = 1;
let metadataRequest = 0;
let submitRequest = 0;
let sourceRequest = 0;
const resultRequests = reactive<Record<string, number>>({});

const selectedStrategy = computed(() => props.strategies.find(item => item.strategy_id === form.strategy_id));
const dslPreview = computed(() => selectedStrategy.value ? parseDSL(selectedStrategy.value.dsl_yaml).preview : null);
const sourceView = computed(() => views.value.find(view => view.view_id === form.source_view_id));
const compatibleSets = computed(() => sourceView.value ? validFactorSets(factorSets.value, sourceView.value, form.frequency) : []);
const bindingsJson = computed(() => sourceView.value ? buildInputBindings(sourceView.value, form.frequency || dslPreview.value?.bar || "", completedSelections()) : "{}");

function close() {
  submitRequest += 1;
  saving.value = false;
  emit("update:visible", false);
}

async function loadAllViews(spaceId: string) {
  const items: View[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listViews({ space_id: spaceId, status: "active", page: { page, size: 200 } });
    items.push(...(rsp.views || []));
    if (!rsp.page_result?.has_more || !(rsp.views || []).length) return items;
  }
}

async function loadAllFactorSets() {
  const items: FactorSetInfo[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listFactorSets({ page: { page, size: 200 } });
    items.push(...(rsp.factor_sets || []));
    if (!rsp.page_result?.has_more || !(rsp.factor_sets || []).length) return items;
  }
}

async function loadAllAccounts() {
  const items: LogicalAccount[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listLogicalAccounts({ page, size: 200 });
    items.push(...(rsp.logical_accounts || []));
    if (!rsp.page_result?.has_more || !(rsp.logical_accounts || []).length) return items;
  }
}

async function loadMetadata() {
  const spaceId = props.spaceId;
  if (!spaceId) return;
  const requestId = ++metadataRequest;
  metadataLoading.value = true;
  error.value = "";
  try {
    const [nextViews, nextSets, nextAccounts] = await Promise.all([loadAllViews(spaceId), loadAllFactorSets(), loadAllAccounts()]);
    if (requestId !== metadataRequest || props.spaceId !== spaceId) return;
    views.value = nextViews.filter(view => view.space_id === spaceId);
    factorSets.value = nextSets.filter(info => info.factor_set?.space_id === spaceId);
    accounts.value = nextAccounts.filter(account => account.space_id === spaceId);
  } catch (err) {
    if (requestId === metadataRequest) error.value = err instanceof Error ? err.message : "元数据加载失败";
  } finally {
    if (requestId === metadataRequest) metadataLoading.value = false;
  }
}

async function loadSourceColumns() {
  const requestId = ++sourceRequest;
  const spaceId = props.spaceId;
  const source = sourceView.value;
  sourceFrequencyError.value = "";
  form.frequency = dslPreview.value?.bar || "";
  factors.value = [];
  bindingReason.value = "";
  if (!source) return;
  try {
    const dataset = await getDataset({ space_id: spaceId, dataset_id: source.dataset_id });
    if (requestId !== sourceRequest || props.spaceId !== spaceId) return;
    const viewFrequency = source.freq || "";
    if (!isTimeSeriesDataKind(dataset.dataset?.data_kind)) sourceFrequencyError.value = "策略源 View 必须基于时序数据集";
    else if (!viewFrequency) sourceFrequencyError.value = "策略源 View 缺少周期";
    else if (normalizeFrequency(viewFrequency) !== normalizeFrequency(form.frequency)) {
      sourceFrequencyError.value = `源 View 周期为 ${viewFrequency}，与 DSL data.bar ${form.frequency} 不一致`;
    }
  } catch (err) {
    if (requestId === sourceRequest && props.spaceId === spaceId) {
      sourceFrequencyError.value = err instanceof Error ? `无法读取源 View 周期：${err.message}` : "无法读取源 View 周期";
    }
  }
}

async function loadViewColumns(viewId: string, spaceId = props.spaceId) {
  const items: ViewColumn[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listViewColumns({ space_id: spaceId, view_id: viewId, page: { page, size: 500 } });
    items.push(...(rsp.columns || []));
    if (!rsp.page_result?.has_more || !(rsp.columns || []).length) break;
  }
  return items;
}

function factorSetFor(id: string) {
  return factorSets.value.find(info => info.factor_set.set_id === id)?.factor_set;
}

function factorsForSet(id: string) {
  return enabledFactors(factorSets.value.find(info => info.factor_set.set_id === id));
}

function factorFor(row: FactorRow) {
  return factorsForSet(row.set_id).find(item => item.factor_id === row.factor_id);
}

function resultColumns(row: FactorRow) {
  const result = resultViews[row.set_id];
  const factor = factorFor(row);
  if (!result || !factor) return [];
  const matches = factor.outputs.map(output => findOutputColumn(result.columns, factor.factor_id, output)).filter(Boolean) as ViewColumn[];
  return row.output ? matches.filter(column => column.attributes?.factor_output === row.output) : matches;
}

async function setChanged(row: FactorRow) {
  row.factor_id = "";
  row.output = "";
  row.column_name = "";
  await loadResultView(row.set_id);
}

function factorChanged(row: FactorRow) {
  row.output = "";
  row.column_name = "";
}

async function loadResultView(setId: string) {
  const set = factorSetFor(setId);
  if (!set || resultViews[setId]) return;
  const requestId = (resultRequests[setId] || 0) + 1;
  resultRequests[setId] = requestId;
  try {
    const items: View[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listViews({ space_id: set.space_id, dataset_id: set.result_dataset_id, status: "active", page: { page, size: 200 } });
      items.push(...(rsp.views || []));
      if (!rsp.page_result?.has_more || !(rsp.views || []).length) break;
    }
    if (requestId !== resultRequests[setId] || props.spaceId !== set.space_id) return;
    const view = items.find(item =>
      item.attributes?.owner_module === "factor" && item.attributes?.view_role === "factor_result"
    );
    if (!view) {
      error.value = `计算任务 ${setId} 的结果 View 尚未就绪`;
      return;
    }
    const columns = await loadViewColumns(view.view_id, set.space_id);
    if (requestId === resultRequests[setId] && props.spaceId === set.space_id) resultViews[setId] = { view, columns };
  } catch (err) {
    if (requestId === resultRequests[setId] && props.spaceId === set.space_id) {
      error.value = err instanceof Error ? err.message : `计算任务 ${setId} 的结果 View 加载失败`;
    }
  }
}

function resultViewId(setId: string) {
  return resultViews[setId]?.view.view_id || "";
}

function completedSelections(): BindingSelection[] {
  return factors.value.map(row => {
    const factor = factorFor(row);
    const factorSet = factorSetFor(row.set_id);
    const result_view_id = resultViewId(row.set_id);
    return factor && factorSet && row.output && row.column_name && result_view_id
      ? { factor, factorSet, output: row.output, column_name: row.column_name, result_view_id }
      : null;
  }).filter((value): value is BindingSelection => value !== null);
}

function addFactor() {
  factors.value.push({ key: nextKey++, set_id: "", factor_id: "", output: "", column_name: "" });
}

function removeFactor(index: number) {
  factors.value.splice(index, 1);
}

function resetBindings() {
  form.source_view_id = "";
  form.frequency = "";
  factors.value = [];
  bindingReason.value = "";
  sourceFrequencyError.value = "";
}

function resetForm() {
  submitRequest += 1;
  saving.value = false;
  form.instance_id = "";
  form.strategy_id = "";
  form.source_view_id = "";
  form.frequency = "";
  form.logical_account_id = "";
  factors.value = [];
  views.value = [];
  factorSets.value = [];
  accounts.value = [];
  Object.keys(resultViews).forEach(key => delete resultViews[key]);
  Object.keys(resultRequests).forEach(key => delete resultRequests[key]);
  bindingReason.value = "";
  sourceFrequencyError.value = "";
}

function validateStep() {
  error.value = "";
  if (step.value === 1 && (!form.instance_id.trim() || !form.strategy_id)) {
    error.value = "请填写实例 ID 并选择策略定义";
    return false;
  }
  if (step.value !== 2) return true;
  const source = sourceView.value;
  if (!source) { error.value = "请选择源 View"; return false; }
  if (sourceFrequencyError.value) { error.value = sourceFrequencyError.value; return false; }
  if (!form.frequency.trim() || normalizeFrequency(form.frequency) !== normalizeFrequency(dslPreview.value?.bar || "")) {
    error.value = `实例频率必须与 DSL data.bar (${dslPreview.value?.bar || "未配置"}) 一致`;
    return false;
  }
  const selections = completedSelections();
  if (selections.length !== factors.value.length) { error.value = "每个因子都必须选择计算任务、定义、输出和结果列"; return false; }
  const selectedOutputs = new Set(selections.flatMap(selection => [selection.factor.factor_id, selection.output, selection.column_name, ...(selection.factor.input_columns || [])]));
  const missingFields = requiredFactorFields(selectedStrategy.value?.dsl_yaml || "").filter(field => !selectedOutputs.has(field));
  if (missingFields.length) { error.value = `DSL 需要因子输出 ${missingFields.join("、")}，请完成对应选择`; return false; }
  const aliasError = validateAliasConflicts(selections);
  if (aliasError) { error.value = aliasError; return false; }
  const selectedFactorIds = factors.value.map(row => row.factor_id).filter(Boolean);
  if (new Set(selectedFactorIds).size !== selectedFactorIds.length) { error.value = "同一个因子不能重复添加"; return false; }
  const result = canCombineSelections(selections, source);
  if (!result.ok) { bindingReason.value = result.reason || "计算任务不兼容"; error.value = bindingReason.value; return false; }
  if (selections.length && !["viewdataready", "view.data.ready", "ready", "event.storage.view.data.ready"].includes((dslPreview.value?.eventName || "").trim().toLowerCase())) {
    error.value = "绑定因子需要 DSL 配置 ViewDataReady 事件；纯定时触发不能运行因子策略";
    return false;
  }
  return true;
}

function next() {
  if (!validateStep()) return;
  step.value += 1;
}

async function submit() {
  if (!validateStep()) return;
  const spaceId = props.spaceId;
  const requestId = ++submitRequest;
  const source = sourceView.value;
  const selections = completedSelections();
  if (!source || source.space_id !== spaceId || selections.some(selection => selection.factorSet.space_id !== spaceId)) {
    error.value = "空间已切换，请重新选择源 View 和计算任务";
    return;
  }
  saving.value = true;
  error.value = "";
  try {
    const response = await createInstance({
      instance_id: form.instance_id.trim(),
      strategy_id: form.strategy_id,
      space_id: spaceId,
      input_bindings_json: buildInputBindings(source, form.frequency, selections),
      logical_account_id: form.logical_account_id
    });
    if (requestId !== submitRequest || props.spaceId !== spaceId) {
      Message.info("实例已创建，但当前空间已切换；请在原空间实例列表中确认");
      return;
    }
    emit("created", response.instance.instance_id);
    close();
    Message.success("策略实例已创建并保持停用");
  } catch (err) {
    if (requestId !== submitRequest || props.spaceId !== spaceId) return;
    try {
      await getInstance(form.instance_id.trim());
      error.value = "创建请求结果未知，但实例 ID 已存在，请返回列表确认，不要重复创建";
    } catch {
      error.value = err instanceof Error ? err.message : "策略实例创建失败";
    }
  } finally {
    if (requestId === submitRequest) saving.value = false;
  }
}

watch(() => props.visible, value => {
  if (value) {
    resetForm();
    step.value = 1;
    error.value = "";
    loadMetadata();
  }
});

watch(() => props.spaceId, (value, previous) => {
  if (value !== previous && props.visible) {
    resetForm();
    step.value = 1;
    loadMetadata();
  }
});
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

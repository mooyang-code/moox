<template>
  <div class="moox-page factor-editor-page">
    <div class="moox-inner">
      <div class="editor-head">
        <div class="editor-head__title">
          <a-button type="text" @click="leave">
            <template #icon><icon-left /></template>
            返回因子定义
          </a-button>
          <h2>{{ editing ? `编辑因子 ${factorId}` : "新建因子" }}</h2>
        </div>
        <a-space>
          <a-button :disabled="saving" @click="leave">取消</a-button>
          <a-tooltip :content="blockerText" :disabled="!blockerText">
            <span>
              <a-button type="primary" :loading="saving" :disabled="Boolean(blockerText)" @click="save">保存因子</a-button>
            </span>
          </a-tooltip>
        </a-space>
      </div>
      <p class="editor-sub">
        因子定义只描述算法本身（代码、参数、输入输出），不属于任何计算任务；有未保存的修改时，离开页面会二次确认。
      </p>

      <a-alert v-if="loadError" type="error" show-icon class="editor-alert">
        {{ loadError }}
        <template #action><a-button size="mini" @click="load">重试</a-button></template>
      </a-alert>
      <a-alert v-if="saveError" type="error" show-icon closable class="editor-alert" @close="saveError = ''">{{
        saveError
      }}</a-alert>
      <a-alert v-if="readOnly" type="warning" show-icon class="editor-alert">
        该因子在 {{ enabledUsages.length }} 个计算任务中处于启用状态，需要先停用后才能编辑。
        <div class="usage-row">
          <button v-for="chip in enabledUsages" :key="chip.setId" type="button" class="usage-chip" @click="openTask(chip.setId)">
            {{ chip.label }}
          </button>
          <a-button size="mini" type="primary" @click="openTask('')">去计算任务</a-button>
        </div>
      </a-alert>

      <a-spin :loading="loading" class="editor-spin">
        <div class="editor-grid">
          <a-form class="editor-form" layout="vertical" :model="form" :disabled="readOnly">
            <a-form-item field="factor_id" label="因子 ID" required>
              <a-input v-model="form.factor_id" :disabled="editing || readOnly" placeholder="Bias" />
            </a-form-item>
            <a-form-item field="name" label="模块名" required>
              <a-input v-model="form.name" placeholder="Bias" />
            </a-form-item>
            <a-form-item field="factor_type" label="因子类型" required>
              <a-radio-group v-model="form.factor_type" type="button" :disabled="readOnly" @change="onTypeChange">
                <a-radio value="timeseries">时序</a-radio>
                <a-radio value="cross_section">横截面</a-radio>
              </a-radio-group>
            </a-form-item>
            <a-form-item field="lookback_periods" label="回看周期" required>
              <a-input-number v-model="form.lookback_periods" :min="1" :precision="0" />
            </a-form-item>
            <a-form-item v-if="form.factor_type === 'cross_section'" field="allow_partial_universe" label="截面计算">
              <a-checkbox v-model="form.allow_partial_universe">允许部分对象参与计算（上游缺失对象时不跳过整个截面）</a-checkbox>
            </a-form-item>

            <a-form-item
              field="input_columns"
              label="输入列"
              required
              extra="只需写列名；加入计算任务时，会按该任务的源数据集校验这些列是否存在。"
            >
              <a-input-tag v-model="form.input_columns" allow-clear placeholder="输入列名后回车" />
            </a-form-item>
            <a-form-item label="参考数据集（可选，仅用于提示可选列，不会保存）">
              <a-select
                v-model="referenceDatasetId"
                allow-search
                allow-clear
                :loading="datasetsLoading"
                placeholder="选一个数据集，查看它有哪些列"
                @change="loadReferenceColumns"
              >
                <a-option v-for="dataset in referenceDatasets" :key="dataset.dataset_id" :value="dataset.dataset_id">
                  {{ dataset.name || dataset.dataset_id }}
                </a-option>
              </a-select>
              <div v-if="referenceColumns.length" class="reference-columns">
                <a-tag
                  v-for="name in referenceColumns"
                  :key="name"
                  class="reference-tag"
                  :class="{ 'is-picked': form.input_columns.includes(name) }"
                  @click="pickColumn(name)"
                >
                  {{ name }}
                </a-tag>
              </div>
            </a-form-item>

            <a-form-item
              field="outputs"
              label="输出列"
              required
              extra="不得与系统保留列重名；加入计算任务时，还会检查与该任务源数据集列、其他因子输出是否重名。"
            >
              <a-input-tag v-model="form.outputs" allow-clear placeholder="输入输出列名后回车" />
            </a-form-item>
            <a-form-item field="params_json" label="参数 JSON" required>
              <a-textarea v-model="form.params_json" :auto-size="{ minRows: 3, maxRows: 8 }" class="params-input" />
            </a-form-item>
          </a-form>

          <div class="editor-side">
            <div class="code-panel">
              <div class="code-panel__tab">
                <span class="code-panel__file">{{ fileName }}</span>
                <span class="code-panel__meta">Python · UTF-8 · 缩进 4 空格</span>
                <a-button v-if="!readOnly" size="mini" class="code-panel__template" @click="insertTemplate">插入模板</a-button>
              </div>
              <AsyncCodeEditor
                v-model="form.source_code"
                :read-only="readOnly"
                :min-lines="18"
                :max-lines="30"
                aria-label="因子源码"
                @cursor-change="cursor = $event"
              />
              <div class="code-panel__status">
                <span>行 {{ cursor.line }}，列 {{ cursor.column }}</span>
                <span :class="{ 'is-error': sourceCheck === 'failed' }">{{ sourceCheckText }}</span>
              </div>
            </div>

            <section class="checklist" aria-label="保存前检查">
              <h3>保存前检查</h3>
              <ul>
                <li v-for="entry in checklist" :key="entry.key" :class="entry.ok ? 'is-ok' : 'is-fail'">
                  <icon-check-circle-fill v-if="entry.ok" />
                  <icon-close-circle-fill v-else />
                  <span
                    >{{ entry.label }}<em v-if="!entry.ok">：{{ entry.message }}</em></span
                  >
                </li>
              </ul>
              <p class="checklist__note">保存后因子定义不属于任何计算任务；到「计算任务」里添加并启用。</p>
            </section>
          </div>
        </div>
      </a-spin>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { createFactor, getFactor, updateFactor } from "@/api/factor";
import type { FactorSetInfo, FactorUsage } from "@/api/factor/types";
import { listDatasetColumns, listDatasets } from "@/api/storage/metadata";
import type { Dataset } from "@/api/storage/types";
import { AsyncCodeEditor } from "@/components/code-editor/async";
import { RequestGate } from "@/utils/request-gate";
import { useFactorScope } from "@/views/factor/shared/use-factor-scope";
import { FACTOR_SOURCE_TEMPLATE } from "./editor-template";
import {
  blankFactorForm,
  buildFactorPayload,
  checklistBlockers,
  factorChecklist,
  formFromDefinition,
  sourceInputColumns
} from "./factor-form";
import { isTimeSeriesDataKind } from "@/views/data/shared/metadata-utils";
import { useSpaceStore } from "@/store/modules/space";

defineOptions({ name: "FactorEditor" });

const PAGE_SIZE = 500;

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const { store } = useFactorScope({ poll: false });

const factorId = computed(() => String(route.params.factorId || ""));
const editing = computed(() => Boolean(factorId.value));

const form = reactive({ ...blankFactorForm(), source_code: FACTOR_SOURCE_TEMPLATE });
const original = ref(JSON.stringify(form));
const usages = ref<FactorUsage[]>([]);
const loading = ref(false);
const loadError = ref("");
const saving = ref(false);
const saveError = ref("");
const sourceCheck = ref<"unchecked" | "failed">("unchecked");
const cursor = ref({ line: 1, column: 1 });
const committed = ref(false);
const loadGate = new RequestGate();

const referenceDatasetId = ref("");
const referenceDatasets = ref<Dataset[]>([]);
const referenceColumns = ref<string[]>([]);
const datasetsLoading = ref(false);
const columnsGate = new RequestGate();

const dirty = computed(() => JSON.stringify(form) !== original.value);
const checklist = computed(() => factorChecklist(form));
const blockerText = computed(() =>
  readOnly.value ? "该因子正在被启用的计算任务使用，需先停用" : checklistBlockers(checklist.value)[0] || ""
);
const fileName = computed(() => `${form.factor_id.trim() || "untitled"}.py`);
const sourceCheckText = computed(() =>
  sourceCheck.value === "failed" ? "源码检查未通过，见上方提示" : "保存时由后端试加载检查源码"
);

function setLabelOf(setId: string) {
  const info = store.sets.find((item: FactorSetInfo) => item.factor_set.set_id === setId);
  return info ? store.setLabel(info.factor_set) : setId;
}

const enabledUsages = computed(() =>
  usages.value
    .filter(usage => usage.status === "enabled")
    .map(usage => ({ setId: usage.set_id, label: setLabelOf(usage.set_id) }))
);
const readOnly = computed(() => editing.value && enabledUsages.value.length > 0);

function resetForm(next = { ...blankFactorForm(), source_code: FACTOR_SOURCE_TEMPLATE }) {
  Object.assign(form, next);
  original.value = JSON.stringify(form);
  usages.value = [];
  saveError.value = "";
  sourceCheck.value = "unchecked";
}

async function load() {
  const token = loadGate.next();
  loadError.value = "";
  committed.value = false;
  if (!editing.value) {
    resetForm();
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const rsp = await getFactor(factorId.value);
    if (!loadGate.isCurrent(token)) return;
    resetForm(formFromDefinition(rsp.factor));
    usages.value = rsp.usages || [];
  } catch (error) {
    if (loadGate.isCurrent(token)) loadError.value = error instanceof Error ? error.message : "因子加载失败";
  } finally {
    if (loadGate.isCurrent(token)) loading.value = false;
  }
}

async function loadReferenceDatasets() {
  const spaceId = spaceStore.selectedSpaceId;
  if (!spaceId) return;
  datasetsLoading.value = true;
  try {
    const items: Dataset[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listDatasets({ space_id: spaceId, page: { page, size: PAGE_SIZE } });
      items.push(...(rsp.datasets || []));
      if (!rsp.page_result?.has_more || !(rsp.datasets || []).length) break;
    }
    referenceDatasets.value = items.filter(
      item =>
        item.status === "active" && isTimeSeriesDataKind(item.data_kind) && item.attributes?.dataset_role !== "factor_result"
    );
  } catch {
    referenceDatasets.value = [];
  } finally {
    datasetsLoading.value = false;
  }
}

async function loadReferenceColumns() {
  const token = columnsGate.next();
  referenceColumns.value = [];
  const spaceId = spaceStore.selectedSpaceId;
  if (!referenceDatasetId.value || !spaceId) return;
  try {
    const columns = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listDatasetColumns({
        space_id: spaceId,
        dataset_id: referenceDatasetId.value,
        page: { page, size: PAGE_SIZE }
      });
      columns.push(...(rsp.columns || []));
      if (!rsp.page_result?.has_more || !(rsp.columns || []).length) break;
    }
    if (columnsGate.isCurrent(token)) referenceColumns.value = sourceInputColumns(columns);
  } catch (error) {
    if (columnsGate.isCurrent(token)) Message.error(error instanceof Error ? error.message : "数据集列加载失败");
  }
}

function pickColumn(name: string) {
  if (readOnly.value || form.input_columns.includes(name)) return;
  form.input_columns = [...form.input_columns, name];
}

function onTypeChange() {
  if (form.factor_type !== "cross_section") form.allow_partial_universe = false;
}

function insertTemplate() {
  if (
    form.source_code.trim() &&
    form.source_code !== FACTOR_SOURCE_TEMPLATE &&
    !window.confirm("编辑器里已有内容，确认用模板替换？")
  )
    return;
  form.source_code = FACTOR_SOURCE_TEMPLATE;
}

async function save() {
  saveError.value = "";
  sourceCheck.value = "unchecked";
  if (readOnly.value || checklistBlockers(checklist.value).length) return;
  saving.value = true;
  try {
    const payload = buildFactorPayload(form);
    if (editing.value) await updateFactor(payload);
    else await createFactor(payload);
    committed.value = true;
    Message.success(editing.value ? "因子已保存" : "因子已创建，到计算任务里添加并启用");
    await router.push("/factor/definitions");
  } catch (error) {
    const message = error instanceof Error ? error.message : "保存因子失败";
    saveError.value = message;
    if (/source|源码|compute|load/i.test(message)) sourceCheck.value = "failed";
  } finally {
    saving.value = false;
  }
}

function leave() {
  void router.push("/factor/definitions");
}

function openTask(setId: string) {
  void router.push(setId ? { path: "/factor/tasks", query: { detail: setId } } : "/factor/tasks");
}

const confirmLeave = () => committed.value || !dirty.value || window.confirm("当前因子定义尚未保存，确认离开？");
onBeforeRouteLeave(() => confirmLeave());
onBeforeRouteUpdate(() => confirmLeave());

watch(factorId, () => void load());
onMounted(() => {
  void load();
  void loadReferenceDatasets();
});
</script>

<style scoped lang="scss">
.editor-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
}

.editor-head__title {
  display: flex;
  align-items: center;
  gap: var(--moox-space-2);
}

.editor-head__title h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.editor-sub {
  margin: var(--moox-space-1) 0 var(--moox-space-3);
  color: var(--color-text-3);
  font-size: 13px;
}

.editor-alert {
  margin-bottom: var(--moox-space-3);
}

.editor-spin {
  display: block;
  width: 100%;
}

.editor-grid {
  display: grid;
  grid-template-columns: minmax(320px, 2fr) minmax(0, 3fr);
  gap: var(--moox-space-5);
  align-items: start;
}

.editor-side {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: var(--moox-space-3);
}

.usage-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
  margin-top: var(--moox-space-2);
}

.usage-chip {
  padding: 1px 8px;
  font-size: 12px;
  background: var(--color-fill-2);
  border: 1px solid var(--color-border-2);
  border-radius: 10px;
  cursor: pointer;
}

.reference-columns {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: var(--moox-space-2);
}

.reference-tag {
  cursor: pointer;
}

.reference-tag.is-picked {
  opacity: 0.5;
}

.params-input :deep(textarea) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
}

.code-panel {
  overflow: hidden;
  border: 1px solid #333;
  border-radius: 6px;
  background: #1e1e1e;
}

.code-panel :deep(.code-editor) {
  border: 0;
  border-radius: 0;
}

.code-panel__tab,
.code-panel__status {
  display: flex;
  align-items: center;
  gap: var(--moox-space-3);
  padding: 6px 12px;
  color: #9d9d9d;
  font-size: 12px;
  background: #252526;
}

.code-panel__file {
  color: #d4d4d4;
}

.code-panel__template {
  margin-left: auto;
}

.code-panel__status {
  justify-content: space-between;
}

.code-panel__status .is-error {
  color: #f48771;
}

.checklist h3 {
  margin: 0 0 var(--moox-space-2);
  font-size: 14px;
  font-weight: 600;
}

.checklist ul {
  margin: 0;
  padding: 0;
  list-style: none;
}

.checklist li {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  padding: 2px 0;
  font-size: 13px;
}

.checklist li.is-ok {
  color: rgb(var(--success-6));
}

.checklist li.is-fail {
  color: rgb(var(--danger-6));
}

.checklist em {
  font-style: normal;
}

.checklist__note {
  margin: var(--moox-space-2) 0 0;
  color: var(--color-text-3);
  font-size: 12px;
}

@media (max-width: 900px) {
  .editor-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>

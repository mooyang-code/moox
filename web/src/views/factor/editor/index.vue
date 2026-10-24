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
        因子用表达式定义：类型、输入列、输出列与回看由编译推导，不需要手填。定义不属于任何计算任务；有未保存的修改时，离开页面会二次确认。
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
          <div class="editor-main">
            <a-form class="editor-form" layout="vertical" :model="form" :disabled="readOnly">
              <div class="editor-form__row">
                <a-form-item field="factor_id" label="因子 ID" required>
                  <a-input v-model="form.factor_id" :disabled="editing || readOnly" placeholder="Bias" />
                </a-form-item>
                <a-form-item field="name" label="展示名" required>
                  <a-input v-model="form.name" :max-length="64" placeholder="Bias 偏离" />
                </a-form-item>
                <a-form-item field="category_id" label="分类">
                  <a-select v-model="form.category_id" allow-clear placeholder="未分类（可在因子定义页管理分类）">
                    <a-option v-for="category in categories" :key="category.category_id" :value="category.category_id">
                      {{ category.name }}
                    </a-option>
                  </a-select>
                </a-form-item>
              </div>
            </a-form>

            <div class="code-panel">
              <div class="code-panel__tab">
                <span class="code-panel__file">{{ fileName }}</span>
                <span class="code-panel__meta">YAML · 缩进 2 空格</span>
                <a-space v-if="!readOnly" class="code-panel__template" :size="4">
                  <a-button size="mini" @click="insertTemplate('timeseries')">时序模板</a-button>
                  <a-button size="mini" @click="insertTemplate('cross_section')">截面模板</a-button>
                </a-space>
              </div>
              <AsyncCodeEditor
                ref="editorRef"
                v-model="form.spec"
                language="yaml"
                :read-only="readOnly"
                :error-line="errorLine"
                :min-lines="14"
                :max-lines="28"
                aria-label="因子表达式定义"
                @cursor-change="cursor = $event"
              />
              <div class="code-panel__status">
                <span>行 {{ cursor.line }}，列 {{ cursor.column }}</span>
                <span :class="compileClass">{{ compileText }}</span>
              </div>
            </div>

            <section v-if="derived" class="derived" aria-label="编译结果">
              <a-descriptions :column="{ xs: 1, md: 2 }" size="small" bordered>
                <a-descriptions-item label="类型">{{ factorTypeLabel(derived.factor_type || "") }}</a-descriptions-item>
                <a-descriptions-item label="最大回看周期">{{ derived.lookback_periods }}</a-descriptions-item>
                <a-descriptions-item label="输入列">{{ (derived.input_columns || []).join(", ") }}</a-descriptions-item>
                <a-descriptions-item label="输出列">{{ (derived.outputs || []).join(", ") }}</a-descriptions-item>
                <a-descriptions-item label="可调参数" :span="2">{{ paramText(derived.params) }}</a-descriptions-item>
                <a-descriptions-item label="定义指纹" :span="2">
                  <span class="mono">{{ derived.definition_hash }}</span>
                </a-descriptions-item>
              </a-descriptions>
              <a-checkbox
                v-if="derived.factor_type === 'cross_section'"
                v-model="form.allow_partial_universe"
                :disabled="readOnly"
                class="derived__partial"
              >
                允许部分对象参与计算（上游缺失对象时不跳过整个截面）
              </a-checkbox>
            </section>

            <section class="preview" aria-label="试算">
              <div class="preview__head">
                <h3>试算</h3>
                <span class="preview__hint">在计算任务的源数据上算最近若干期，只读、不写结果。</span>
              </div>
              <div class="preview__controls">
                <a-select v-model="previewSetId" allow-search placeholder="选择计算任务" class="preview__set">
                  <a-option v-for="info in store.sets" :key="info.factor_set.set_id" :value="info.factor_set.set_id">
                    {{ store.setLabel(info.factor_set) }}
                  </a-option>
                </a-select>
                <a-input-tag
                  v-model="previewSubjects"
                  allow-clear
                  placeholder="标的（可选，回车添加，最多 20 个）"
                  class="preview__subjects"
                />
                <a-input-number v-model="previewPeriods" :min="1" :max="500" :precision="0" class="preview__periods">
                  <template #suffix>期</template>
                </a-input-number>
                <a-button type="primary" :loading="previewing" :disabled="!canPreview" @click="runPreview">试算</a-button>
              </div>
              <a-alert v-if="previewError" type="error" show-icon class="preview__error">{{ previewError }}</a-alert>
              <a-table
                v-if="previewRows.length"
                :data="previewRows"
                :pagination="{ pageSize: 20, size: 'mini' }"
                size="small"
                :bordered="{ cell: true }"
                row-key="key"
                :scroll="{ x: 'max-content' }"
              >
                <template #columns>
                  <a-table-column title="标的" data-index="subject_id" />
                  <a-table-column title="标签" data-index="series_tag" />
                  <a-table-column title="时间" data-index="time" />
                  <a-table-column v-for="(column, index) in previewColumns" :key="column" :title="column">
                    <template #cell="{ record }">
                      <span :class="{ 'is-null': record.cells[index] === null }">{{ formatCell(record.cells[index]) }}</span>
                    </template>
                  </a-table-column>
                </template>
              </a-table>
              <p v-else-if="previewDone && !previewError" class="preview__empty">这段时间没有源数据行。</p>
            </section>
          </div>

          <div class="editor-side">
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
              <p class="checklist__note">
                保存后因子定义不属于任何计算任务；到「计算任务」里添加并启用，加入时会按源数据集校验输入列。
              </p>
            </section>

            <section class="functions" aria-label="函数参考">
              <div class="functions__head">
                <h3>函数参考</h3>
                <span v-if="exprVersion" class="functions__version">{{ exprVersion }}</span>
              </div>
              <a-input-search v-model="functionKeyword" allow-clear placeholder="搜索函数名或说明" size="small" />
              <a-alert v-if="functionsError" type="warning" class="functions__error">{{ functionsError }}</a-alert>
              <a-collapse :default-active-key="['timeseries']" :bordered="false" class="functions__groups">
                <a-collapse-item
                  v-for="group in functionGroups"
                  :key="group.category"
                  :header="`${group.label}（${group.items.length}）`"
                >
                  <ul class="functions__list">
                    <li v-for="fn in group.items" :key="fn.name">
                      <button type="button" class="functions__name" :disabled="readOnly" @click="insertFunction(fn)">
                        {{ fn.signature }}
                      </button>
                      <p class="functions__doc">{{ fn.doc }}</p>
                      <p v-if="fn.lookback" class="functions__meta">回看：{{ fn.lookback }}</p>
                      <code v-if="fn.example" class="functions__example">{{ fn.example }}</code>
                    </li>
                  </ul>
                </a-collapse-item>
              </a-collapse>
            </section>

            <section class="reference" aria-label="参考数据集">
              <h3>参考数据集</h3>
              <p class="reference__hint">可选，仅用于查看有哪些列，点击列名插入到光标处，不会保存。</p>
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
                  :class="{ 'is-picked': usedColumns.has(name) }"
                  @click="pickColumn(name)"
                >
                  {{ name }}
                </a-tag>
              </div>
            </section>
          </div>
        </div>
      </a-spin>
    </div>
  </div>
</template>

<script setup lang="ts">
import { createSubmitScope } from "@/utils/submit-scope";
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { computed, onMounted, reactive, ref, watch, onBeforeUnmount } from "vue";
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import {
  createFactor,
  getFactor,
  listFactorCategories,
  listFactorFunctions,
  previewFactor,
  updateFactor,
  validateFactorSpec
} from "@/api/factor";
import type {
  FactorCategory,
  FactorParamDecl,
  FactorFunction,
  FactorSetInfo,
  FactorUsage,
  PreviewFactorRsp,
  ValidateFactorSpecRsp
} from "@/api/factor/types";
import { listDatasetColumns, listDatasets } from "@/api/storage/metadata";
import type { Dataset } from "@/api/storage/types";
import { AsyncCodeEditor } from "@/components/code-editor/async";
import { RequestGate } from "@/utils/request-gate";
import { useFactorScope } from "@/views/factor/shared/use-factor-scope";
import { factorTypeLabel } from "@/views/factor/shared/status";
import { isBuiltinTemplate, specTemplate, type FactorTemplateKind } from "./editor-template";
import { describeParams } from "@/views/factor/shared/params";
import {
  FUNCTION_CATEGORY_LABELS,
  blankFactorForm,
  buildFactorPayload,
  checklistBlockers,
  compiledFor,
  diagnosticDocLine,
  diagnosticText,
  factorChecklist,
  filterFunctions,
  formFromDefinition,
  sourceInputColumns,
  specKey,
  type CompileState
} from "./factor-form";
import { isTimeSeriesDataKind } from "@/views/data/shared/metadata-utils";
import { useSpaceStore } from "@/store/modules/space";
import { createLatestRequestGuard } from "@/utils/latest-request";

defineOptions({ name: "FactorEditor" });

const PAGE_SIZE = 500;
/** 输入停止多久后校验定义。 */
const VALIDATE_DELAY_MS = 400;

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const { store } = useFactorScope({ poll: false });

const factorId = computed(() => String(route.params.factorId || ""));
const editing = computed(() => Boolean(factorId.value));

const form = reactive(blankFactorForm());
const original = ref(JSON.stringify(form));
const usages = ref<FactorUsage[]>([]);
const loading = ref(false);
const loadError = ref("");
const saving = ref(false);
const saveError = ref("");
const cursor = ref({ line: 1, column: 1 });
const committed = ref(false);
const loadGate = new RequestGate();
const editorRef = ref<{ insertAtCursor(text: string): boolean } | null>(null);

const referenceDatasetId = ref("");
const referenceDatasets = ref<Dataset[]>([]);
const referenceColumns = ref<string[]>([]);
const datasetsLoading = ref(false);
const columnsGate = new RequestGate();

const compile = ref<CompileState>({ status: "idle" });
const validateGate = new RequestGate();
let validateTimer: ReturnType<typeof setTimeout> | undefined;

const functions = ref<FactorFunction[]>([]);
const exprVersion = ref("");
const functionsError = ref("");
const functionKeyword = ref("");

const previewSetId = ref("");
const previewSubjects = ref<string[]>([]);
const previewPeriods = ref(20);
const previewing = ref(false);
const previewError = ref("");
const previewDone = ref(false);
const previewResult = ref<PreviewFactorRsp | null>(null);
const previewGate = new RequestGate();

const dirty = computed(() => JSON.stringify(form) !== original.value);
const checklist = computed(() => factorChecklist(form, compile.value));
const blockerText = computed(() =>
  readOnly.value ? "该因子正在被启用的计算任务使用，需先停用" : checklistBlockers(checklist.value)[0] || ""
);
const fileName = computed(() => `${form.factor_id.trim() || "untitled"}.yaml`);
/** 最近一次通过的编译结果；定义改动后、新结果回来前继续显示旧结果，避免面板闪烁。 */
const lastValid = ref<ValidateFactorSpecRsp | null>(null);
const derived = computed(() => (compile.value.status === "valid" ? compile.value.result : lastValid.value));
const paramText = (params?: FactorParamDecl[]) => describeParams(params);
const errorLine = computed(() =>
  compile.value.status === "invalid" && compile.value.spec === specKey(form) ? diagnosticDocLine(compile.value.result) : 0
);
const compileText = computed(() => {
  const state = compile.value;
  if (!form.spec.trim()) return "定义为空";
  if (state.status === "checking") return "校验中…";
  if (state.status === "error") return `校验失败：${state.message}`;
  if (state.status === "valid") return state.spec === specKey(form) ? "编译通过" : "等待校验";
  if (state.status === "invalid") return diagnosticText(state.result);
  return "等待校验";
});
const compileClass = computed(() => ({
  "is-error": compile.value.status === "invalid" || compile.value.status === "error",
  "is-ok": compile.value.status === "valid" && compiledFor(compile.value, form)
}));
const usedColumns = computed(() => new Set(derived.value?.input_columns || []));
const canPreview = computed(() => Boolean(previewSetId.value) && compiledFor(compile.value, form) && !previewing.value);
const previewColumns = computed(() => previewResult.value?.columns || []);
const previewRows = computed(() =>
  (previewResult.value?.rows || []).map((row, index) => ({
    key: index,
    subject_id: row.subject_id,
    series_tag: row.series_tag || "-",
    time: formatTime(row.data_time),
    cells: (row.values || []).map((value, i) => (row.nulls?.[i] ? null : value))
  }))
);
const functionGroups = computed(() => {
  const visible = filterFunctions(functions.value, functionKeyword.value);
  return ["timeseries", "cross_section", "elementwise"]
    .map(category => ({
      category,
      label: FUNCTION_CATEGORY_LABELS[category] || category,
      items: visible.filter(fn => fn.category === category)
    }))
    .filter(group => group.items.length);
});

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

function formatTime(value: string) {
  const at = new Date(value);
  return Number.isNaN(at.getTime()) ? value : at.toLocaleString("zh-CN", { hour12: false });
}

function formatCell(value: number | null) {
  if (value === null) return "null";
  // 8 位有效数字；转回数字再输出可去掉末尾的 0，且不破坏科学记数法的指数。
  return Number.isInteger(value) ? String(value) : String(Number(value.toPrecision(8)));
}

function resetForm(next = blankFactorForm()) {
  Object.assign(form, next);
  original.value = JSON.stringify(form);
  usages.value = [];
  saveError.value = "";
  previewResult.value = null;
  previewDone.value = false;
  previewError.value = "";
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
    // 切换空间造成的取消不是失败：空间监听会重新加载
    if (loadGate.isCurrent(token) && !(error instanceof StaleSpaceRequestError)) {
      loadError.value = error instanceof Error ? error.message : "因子加载失败";
    }
  } finally {
    if (loadGate.isCurrent(token)) loading.value = false;
  }
}

/** 定义改动后防抖校验；只采用最后一次请求的结果。 */
function scheduleValidate() {
  if (validateTimer) clearTimeout(validateTimer);
  validateGate.next();
  if (!form.spec.trim()) {
    compile.value = { status: "idle" };
    return;
  }
  validateTimer = setTimeout(() => void validateNow(), VALIDATE_DELAY_MS);
}

async function validateNow() {
  const token = validateGate.next();
  const key = specKey(form);
  compile.value = { status: "checking" };
  try {
    const result = await validateFactorSpec(form.spec, form.allow_partial_universe);
    if (!validateGate.isCurrent(token)) return;
    if (result.valid) {
      compile.value = { status: "valid", spec: key, result };
      lastValid.value = result;
      if (result.factor_type !== "cross_section" && form.allow_partial_universe) form.allow_partial_universe = false;
    } else {
      compile.value = { status: "invalid", spec: key, result };
    }
  } catch (error) {
    if (validateGate.isCurrent(token)) {
      compile.value = { status: "error", message: error instanceof Error ? error.message : "接口调用失败" };
    }
  }
}

const categories = ref<FactorCategory[]>([]);

/** 分类只用于归类与筛选；加载失败不影响编辑与保存（保存时不带分类即保持原分类）。 */
async function loadCategories() {
  try {
    categories.value = await listFactorCategories();
  } catch {
    categories.value = [];
  }
}

async function loadFunctions() {
  functionsError.value = "";
  try {
    const rsp = await listFactorFunctions();
    functions.value = rsp.functions || [];
    exprVersion.value = rsp.expr_version || "";
  } catch (error) {
    functionsError.value = error instanceof Error ? `函数清单加载失败：${error.message}` : "函数清单加载失败";
  }
}

async function runPreview() {
  if (!canPreview.value) return;
  const token = previewGate.next();
  previewing.value = true;
  previewError.value = "";
  previewDone.value = false;
  try {
    const rsp = await previewFactor({
      set_id: previewSetId.value,
      spec: form.spec,
      subjects: previewSubjects.value,
      periods: previewPeriods.value,
      allow_partial_universe: form.allow_partial_universe
    });
    if (!previewGate.isCurrent(token)) return;
    previewResult.value = rsp;
    previewDone.value = true;
  } catch (error) {
    if (previewGate.isCurrent(token)) {
      previewResult.value = null;
      previewError.value = error instanceof Error ? error.message : "试算失败";
    }
  } finally {
    if (previewGate.isCurrent(token)) previewing.value = false;
  }
}

const referenceGuard = createLatestRequestGuard();

async function loadReferenceDatasets() {
  const spaceId = spaceStore.selectedSpaceId;
  const request = referenceGuard.begin();
  if (!spaceId) {
    datasetsLoading.value = false;
    return;
  }
  datasetsLoading.value = true;
  try {
    const items: Dataset[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listDatasets({ space_id: spaceId, page: { page, size: PAGE_SIZE } });
      if (!request.isLatest() || spaceId !== spaceStore.selectedSpaceId) return;
      items.push(...(rsp.datasets || []));
      if (!rsp.page_result?.has_more || !(rsp.datasets || []).length) break;
    }
    referenceDatasets.value = items.filter(
      item =>
        item.status === "active" && isTimeSeriesDataKind(item.data_kind) && item.attributes?.dataset_role !== "factor_result"
    );
  } catch {
    if (request.isLatest()) referenceDatasets.value = [];
  } finally {
    if (request.isLatest()) datasetsLoading.value = false;
  }
}

function clearReferenceSelection() {
  columnsGate.next();
  referenceDatasetId.value = "";
  referenceColumns.value = [];
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
      if (!columnsGate.isCurrent(token) || spaceId !== spaceStore.selectedSpaceId) return;
      columns.push(...(rsp.columns || []));
      if (!rsp.page_result?.has_more || !(rsp.columns || []).length) break;
    }
    if (columnsGate.isCurrent(token)) referenceColumns.value = sourceInputColumns(columns);
  } catch (error) {
    if (columnsGate.isCurrent(token)) Message.error(error instanceof Error ? error.message : "数据集列加载失败");
  }
}

/** 在光标处插入文本；编辑器还没加载完时追加到末尾。 */
function insertText(text: string) {
  if (readOnly.value) return;
  if (!editorRef.value?.insertAtCursor(text)) form.spec = `${form.spec}${text}`;
}

function pickColumn(name: string) {
  insertText(name);
}

function insertFunction(fn: FactorFunction) {
  insertText(fn.signature);
}

function insertTemplate(kind: FactorTemplateKind) {
  if (form.spec.trim() && !isBuiltinTemplate(form.spec) && !window.confirm("编辑器里已有内容，确认用模板替换？")) return;
  form.spec = specTemplate(kind);
}

async function save() {
  saveError.value = "";
  if (readOnly.value || checklistBlockers(checklist.value).length) return;
  const state = compile.value;
  if (state.status !== "valid") return;
  saving.value = true;
  const submission = saveScope.begin();
  try {
    const payload = buildFactorPayload(form, state.result.factor_type);
    if (editing.value) await updateFactor(payload);
    else await createFactor(payload);
    committed.value = true;
    Message.success(editing.value ? "因子已保存" : "因子已创建，到计算任务里添加并启用");
    // 保存期间用户已确认离开页面或切换了因子：因子已保存，但不能再把用户带回列表
    if (!submission.isCurrent()) return;
    await router.push("/factor/definitions");
  } catch (error) {
    if (!submission.isCurrent()) return;
    saveError.value = error instanceof Error ? error.message : "保存因子失败";
  } finally {
    if (submission.isCurrent()) saving.value = false;
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
watch(() => [form.spec, form.allow_partial_universe], scheduleValidate, { immediate: true });
watch(
  () => store.sets.map((info: FactorSetInfo) => info.factor_set.set_id),
  ids => {
    if (previewSetId.value && !ids.includes(previewSetId.value)) previewSetId.value = "";
    if (!previewSetId.value && ids.length) previewSetId.value = ids[0];
  },
  { immediate: true }
);
// 空间初始化完成或切换后重新加载参考数据集；旧空间的数据集、已选数据集和列一并清空
watch(
  () => spaceStore.selectedSpaceId,
  (spaceId, oldSpaceId) => {
    if (spaceId === oldSpaceId) return;
    referenceGuard.invalidate();
    previewGate.next();
    previewing.value = false;
    previewResult.value = null;
    previewDone.value = false;
    // 定义读取若被切换取消（仍在加载或失败），在新空间下重新加载；已成功加载的定义（可能含未保存修改）不动
    if (editing.value && (loading.value || loadError.value)) void load();
    referenceDatasets.value = [];
    clearReferenceSelection();
    void loadReferenceDatasets();
  }
);
onMounted(() => {
  void load();
  void loadReferenceDatasets();
  void loadFunctions();
  void loadCategories();
});

// 保存作用域：页面卸载或切换因子都会使进行中的保存失效，迟到的响应不能再导航
const saveScope = createSubmitScope({ global: true });
onBeforeUnmount(() => {
  saveScope.invalidate();
  if (validateTimer) clearTimeout(validateTimer);
  validateGate.next();
  previewGate.next();
});
watch(factorId, () => saveScope.invalidate());
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
  grid-template-columns: minmax(0, 3fr) minmax(300px, 2fr);
  gap: var(--moox-space-5);
  align-items: start;
}

.editor-main,
.editor-side {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: var(--moox-space-4);
}

.editor-form__row {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
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

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  word-break: break-all;
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

.code-panel__status .is-ok {
  color: #89d185;
}

.derived__partial {
  margin-top: var(--moox-space-2);
}

.preview__head,
.functions__head {
  display: flex;
  align-items: baseline;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-2);
}

.preview h3,
.functions h3,
.reference h3,
.checklist h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
}

.preview__hint,
.reference__hint,
.functions__version {
  color: var(--color-text-3);
  font-size: 12px;
}

.reference__hint {
  margin: var(--moox-space-1) 0 var(--moox-space-2);
}

.preview__controls {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-2);
}

.preview__set {
  width: 240px;
}

.preview__subjects {
  flex: 1;
  min-width: 200px;
}

.preview__periods {
  width: 110px;
}

.preview__error {
  margin-bottom: var(--moox-space-2);
}

.preview__empty {
  color: var(--color-text-3);
  font-size: 13px;
}

.is-null {
  color: var(--color-text-4);
}

.functions__error {
  margin-top: var(--moox-space-2);
}

.functions__groups {
  margin-top: var(--moox-space-2);
  max-height: 520px;
  overflow-y: auto;
}

.functions__list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.functions__list li {
  padding: 6px 0;
  border-bottom: 1px solid var(--color-border-1);
}

.functions__name {
  padding: 0;
  color: rgb(var(--primary-6));
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  text-align: left;
  background: none;
  border: 0;
  cursor: pointer;
}

.functions__name:disabled {
  color: var(--color-text-2);
  cursor: default;
}

.functions__doc,
.functions__meta {
  margin: 2px 0 0;
  color: var(--color-text-2);
  font-size: 12px;
}

.functions__meta {
  color: var(--color-text-3);
}

.functions__example {
  display: block;
  margin-top: 2px;
  color: var(--color-text-3);
  font-size: 11px;
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

.checklist ul {
  margin: var(--moox-space-2) 0 0;
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

@media (max-width: 1100px) {
  .editor-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}

@media (max-width: 600px) {
  .editor-form__row {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>

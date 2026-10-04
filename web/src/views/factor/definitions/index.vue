<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>因子定义</h2>
        <a-space wrap>
          <a-select v-model="selectedSetId" placeholder="因子集" style="width: 300px" :loading="setsLoading" @change="reloadFirstPage">
            <a-option v-for="set in availableSets" :key="set.set_id" :value="set.set_id">
              {{ set.source_dataset_id }} · {{ set.freq }}（{{ set.set_id }}）
            </a-option>
          </a-select>
          <a-select v-model="filters.status" allow-clear placeholder="状态" style="width: 130px" @change="reloadFirstPage">
            <a-option value="enabled">已启用</a-option>
            <a-option value="disabled">已停用</a-option>
          </a-select>
          <a-button @click="reloadFirstPage">查询</a-button>
            <a-button type="primary" status="success" :disabled="!selectedSetId" @click="openCreate">
            <template #icon><icon-plus /></template>
            新增因子
          </a-button>
        </a-space>
      </div>

      <a-table
        row-key="factor_id"
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="rows"
        :pagination="pagination"
        :scroll="{ x: 'max-content' }"
        @page-change="onPageChange"
        @page-size-change="onPageSizeChange"
      >
        <template #columns>
          <a-table-column title="因子ID" data-index="factor_id" :width="150" />
          <a-table-column title="模块名" data-index="name" :width="130" />
          <a-table-column title="因子类型" :width="110">
            <template #cell="{ record }">{{ factorTypeLabel(record.factor_type) }}</template>
          </a-table-column>
          <a-table-column title="输入列" :width="180" :ellipsis="true" :tooltip="true">
            <template #cell="{ record }">{{ record.input_columns.join(", ") }}</template>
          </a-table-column>
          <a-table-column title="输出列" :width="180" :ellipsis="true" :tooltip="true">
            <template #cell="{ record }">{{ record.outputs.join(", ") }}</template>
          </a-table-column>
          <a-table-column title="回看周期数" data-index="lookback_periods" :width="110" />
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }">
              <a-tag size="small" :color="factorStatusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="更新时间" :width="180">
            <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="260" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-space>
                <a-button size="mini" type="text" @click="openDetail(record)">详情</a-button>
                <a-button size="mini" type="text" :disabled="record.status !== 'disabled'" @click="openEdit(record)">编辑</a-button>
                <a-button size="mini" type="text" @click="toggleStatus(record)">
                  {{ record.status === "enabled" ? "禁用" : "启用" }}
                </a-button>
                <a-popconfirm content="删除因子定义后不可恢复，确认继续？" @ok="remove(record)">
                  <a-button size="mini" type="text" status="danger" :disabled="record.status !== 'disabled'">删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </div>

    <a-drawer v-model:visible="detailVisible" title="因子详情" :width="860">
      <template v-if="selectedFactor">
        <a-descriptions :column="2" bordered size="small">
          <a-descriptions-item label="因子ID">{{ selectedFactor.factor_id }}</a-descriptions-item>
          <a-descriptions-item label="模块名">{{ selectedFactor.name }}</a-descriptions-item>
          <a-descriptions-item label="状态">
            <a-tag size="small" :color="factorStatusColor(selectedFactor.status)">
              {{ statusLabel(selectedFactor.status) }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="回看周期数">{{ selectedFactor.lookback_periods }}</a-descriptions-item>
          <a-descriptions-item label="输入列" :span="2">{{ selectedFactor.input_columns.join(", ") || "-" }}</a-descriptions-item>
          <a-descriptions-item label="输出列" :span="2">{{ selectedFactor.outputs.join(", ") || "-" }}</a-descriptions-item>
          <a-descriptions-item label="源码Hash" :span="2">
            <span class="source-hash">{{ selectedFactor.source_hash || "-" }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="更新时间" :span="2">{{ formatTime(selectedFactor.updated_at) }}</a-descriptions-item>
        </a-descriptions>
        <div class="detail-section">
          <h3>源码</h3>
          <CodeBlock :code="selectedFactor.source_code" language="python" />
        </div>
      </template>
    </a-drawer>

    <a-modal
      v-model:visible="visible"
      width="920px"
      :title="modalTitle"
      :align-center="false"
      :top="'40px'"
      :modal-style="{ maxWidth: 'calc(100vw - 32px)' }"
      :body-style="{ maxHeight: 'calc(100vh - 168px)', overflowY: 'auto', padding: '18px var(--moox-space-6) 14px' }"
      @ok="submit"
    >
      <a-form class="factor-form" :model="form" layout="vertical">
        <a-form-item field="factor_id" label="因子ID" required>
          <a-input v-model="form.factor_id" :disabled="editing" placeholder="Bias" />
        </a-form-item>
        <a-form-item field="name" label="Python 模块名" required>
          <a-input v-model="form.name" placeholder="Bias" />
        </a-form-item>
        <a-form-item field="factor_type" label="因子类型" required>
          <a-select v-model="form.factor_type">
            <a-option value="timeseries">时序因子</a-option>
            <a-option value="cross_section">截面因子</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="form.status" disabled>
            <a-option value="enabled">已启用</a-option>
            <a-option value="disabled">已停用</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="lookback_periods" label="回看周期数" required>
          <a-input-number v-model="form.lookback_periods" :min="1" />
        </a-form-item>
        <a-form-item field="allow_partial_universe" label="截面计算">
          <a-checkbox v-model="form.allow_partial_universe">允许部分对象参与计算</a-checkbox>
        </a-form-item>
        <a-form-item class="form-span-2" field="input_columns" label="输入列" required>
          <a-input-tag v-model="inputTags" allow-clear placeholder="输入列名后回车" />
        </a-form-item>
        <a-form-item class="form-span-2" field="outputs" label="输出列" required>
          <a-input-tag v-model="outputTags" allow-clear placeholder="输入输出列名后回车" />
        </a-form-item>
        <a-form-item class="form-span-2" field="params_json" label="参数 JSON" required>
          <a-textarea v-model="form.params_json" :auto-size="{ minRows: 4, maxRows: 10 }" />
        </a-form-item>
        <a-form-item class="form-span-2" field="source_code" label="源码" required>
          <a-textarea class="code-editor" v-model="form.source_code" :auto-size="{ minRows: 16, maxRows: 28 }" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { createFactor, deleteFactor, listFactorSets, listFactors, setFactorStatus, updateFactor } from "@/api/factor";
import type { FactorDef, FactorSet } from "@/api/factor/types";
import CodeBlock from "@/components/code-block/index.vue";
import { applyPageResult, defaultPagination, formatTime, statusLabel } from "@/views/data/shared/metadata-utils";
import { useSpaceStore } from "@/store/modules/space";
import { validateFactorParamsJSON } from "./factor-form";

defineOptions({ name: "FactorDefinitions" });

const rows = ref<FactorDef[]>([]);
const sets = ref<FactorSet[]>([]);
const loading = ref(false);
const setsLoading = ref(false);
const visible = ref(false);
const detailVisible = ref(false);
const editing = ref(false);
const selectedFactor = ref<FactorDef | null>(null);
const pagination = reactive(defaultPagination());
const filters = reactive({ status: "" as FactorDef["status"] | "" });
const selectedSetId = ref("");
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const availableSets = computed(() => sets.value.filter(set => set.space_id === selectedSpaceId.value));
const inputTags = ref<string[]>(["close"]);
const outputTags = ref<string[]>(["bias_20"]);
let setsLoadSequence = 0;
let factorsLoadSequence = 0;

const form = reactive<FactorDef>({
  factor_id: "",
  set_id: "",
  factor_type: "timeseries",
  name: "",
  source_code: "",
  input_columns: ["close"],
  outputs: ["bias_20"],
  params_json: `{"windows":[20]}`,
  lookback_periods: 200,
  allow_partial_universe: false,
  status: "disabled"
});

const modalTitle = computed(() => (editing.value ? "编辑因子" : "新增因子"));

function factorTypeLabel(type: string) {
  return ({ timeseries: "时序因子", cross_section: "截面因子" } as Record<string, string>)[type] || type;
}

async function load() {
  const sequence = ++factorsLoadSequence;
  const setId = selectedSetId.value;
  if (!setId) {
    rows.value = [];
    pagination.total = 0;
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const rsp = await listFactors({
      set_id: setId,
      status: filters.status || undefined,
      page: { page: pagination.current, size: pagination.pageSize }
    });
    if (sequence !== factorsLoadSequence || setId !== selectedSetId.value) return;
    rows.value = rsp.factors || [];
    applyPageResult(pagination, rsp.page_result);
  } catch (error) {
    if (sequence === factorsLoadSequence) Message.error(error instanceof Error ? error.message : "因子定义加载失败");
  } finally {
    if (sequence === factorsLoadSequence) loading.value = false;
  }
}

function reloadFirstPage() {
  pagination.current = 1;
  load();
}

function resetForm() {
  Object.assign(form, {
    factor_id: "",
    set_id: selectedSetId.value,
    name: "",
    source_code: [
      "def compute(df, params, context):",
      "    close = df['close']",
      "    result = df[['data_time', 'series_tag']].copy()",
      "    for window in params['windows']:",
      "        average = close.rolling(window, min_periods=1).mean()",
      "        result[f'bias_{window}'] = close / average - 1",
      "    return result",
      ""
    ].join("\n"),
    input_columns: ["close"],
    outputs: ["bias_20"],
    params_json: `{"windows":[20]}`,
    lookback_periods: 200,
    allow_partial_universe: false,
    factor_type: "timeseries",
    status: "disabled"
  });
  inputTags.value = ["close"];
  outputTags.value = ["bias_20"];
}

function openCreate() {
  if (!selectedSetId.value) {
    Message.warning("请先选择因子集");
    return;
  }
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openDetail(record: FactorDef) {
  selectedFactor.value = record;
  detailVisible.value = true;
}

function openEdit(record: FactorDef) {
  if (record.status !== "disabled") return;
  editing.value = true;
  Object.assign(form, record);
  inputTags.value = [...(record.input_columns || [])];
  outputTags.value = [...(record.outputs || [])];
  visible.value = true;
}

async function submit() {
  if (!form.factor_id || !form.name || !form.source_code) {
    Message.warning("请补全因子ID、模块名和源码");
    return;
  }
  if (!inputTags.value.length || !outputTags.value.length) {
    Message.warning("输入列和输出列不能为空");
    return;
  }
  let paramsJSON: string;
  try {
    paramsJSON = validateFactorParamsJSON(form.params_json);
  } catch (error) {
    Message.warning(error instanceof SyntaxError ? "参数必须是合法 JSON" : "参数必须是 JSON object");
    return;
  }
  const payload = {
    ...form,
    input_columns: [...inputTags.value],
    outputs: [...outputTags.value],
    params_json: paramsJSON
  };
  payload.set_id = selectedSetId.value;
  try {
    if (editing.value) await updateFactor(payload);
    else await createFactor(payload);
    Message.success("因子已保存");
    visible.value = false;
    await load();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "保存因子失败");
  }
}

async function toggleStatus(record: FactorDef) {
  const next = record.status === "enabled" ? "disabled" : "enabled";
  try {
    await setFactorStatus(record.factor_id, next);
    Message.success("状态已更新");
    await load();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "更新因子状态失败");
  }
}

async function remove(record: FactorDef) {
  try {
    await deleteFactor(record.factor_id);
    Message.success("因子已删除");
    await load();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "删除因子失败");
  }
}

function onPageChange(page: number) {
  pagination.current = page;
  load();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  load();
}

function factorStatusColor(status?: string) {
  if (status === "enabled") return "green";
  if (status === "disabled") return "orange";
  return "gray";
}

onMounted(loadSets);

async function loadSets() {
  const spaceId = selectedSpaceId.value;
  const sequence = ++setsLoadSequence;
  if (!spaceId) {
    sets.value = [];
    selectedSetId.value = "";
    setsLoading.value = false;
    return;
  }
  setsLoading.value = true;
  try {
    const items: FactorSet[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listFactorSets({ page: { page, size: 500 } });
      items.push(...(rsp.factor_sets || []).map(item => item.factor_set).filter((set): set is FactorSet => Boolean(set)));
      if (!rsp.page_result?.has_more || !(rsp.factor_sets || []).length) break;
    }
    if (sequence !== setsLoadSequence || spaceId !== selectedSpaceId.value) return;
    sets.value = items.filter(set => set.space_id === spaceId);
    if (!sets.value.some(set => set.set_id === selectedSetId.value)) {
      selectedSetId.value = sets.value[0]?.set_id || "";
    }
    await load();
  } catch (error) {
    if (sequence === setsLoadSequence) Message.error(error instanceof Error ? error.message : "因子集加载失败");
  } finally {
    if (sequence === setsLoadSequence) setsLoading.value = false;
  }
}

watch(selectedSpaceId, () => {
  selectedSetId.value = "";
  pagination.current = 1;
  loadSets();
});

watch(selectedSetId, () => {
  pagination.current = 1;
  load();
});
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-toolbar-table);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.factor-form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: 18px;
}

.form-span-2 {
  grid-column: span 2;
}

.code-editor :deep(textarea) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  line-height: 1.55;
}

.detail-section {
  margin-top: 20px;
}

.detail-section h3 {
  margin: 0 0 8px;
  font-size: 14px;
}

.source-hash {
  overflow-wrap: anywhere;
  color: var(--color-text-2);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
}

@media (max-width: 768px) {
  .page-head {
    align-items: flex-start;
    flex-direction: column;
  }

  .factor-form {
    grid-template-columns: 1fr;
  }

  .form-span-2 {
    grid-column: span 1;
  }
}
</style>

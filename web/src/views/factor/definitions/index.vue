<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>因子计算</h2>
        <a-space wrap>
          <a-select v-model="filters.status" allow-clear placeholder="状态" style="width: 130px" @change="reloadFirstPage">
            <a-option value="enabled">enabled</a-option>
            <a-option value="disabled">disabled</a-option>
          </a-select>
          <a-button @click="reloadFirstPage">查询</a-button>
          <a-button type="primary" status="success" @click="openCreate">
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
          <a-table-column title="输入列" :width="180" :ellipsis="true" :tooltip="true">
            <template #cell="{ record }">{{ record.input_columns.join(", ") }}</template>
          </a-table-column>
          <a-table-column title="输出列" :width="180" :ellipsis="true" :tooltip="true">
            <template #cell="{ record }">{{ record.outputs.join(", ") }}</template>
          </a-table-column>
          <a-table-column title="回看周期数" data-index="lookback_periods" :width="110" />
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }">
              <a-tag size="small" :color="factorStatusColor(record.status)">{{ record.status }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="更新时间" :width="180">
            <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="260" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-space>
                <a-button size="mini" type="text" @click="openDetail(record)">详情</a-button>
                <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
                <a-button size="mini" type="text" @click="toggleStatus(record)">
                  {{ record.status === "enabled" ? "禁用" : "启用" }}
                </a-button>
                <a-popconfirm content="仅无绑定因子可删除，确认继续？" @ok="remove(record)">
                  <a-button size="mini" type="text" status="danger">删除</a-button>
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
              {{ selectedFactor.status === "enabled" ? "启用" : "停用" }}
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
          <a-input v-model="form.factor_id" :disabled="editing" placeholder="bias" />
        </a-form-item>
        <a-form-item field="name" label="Python 模块名" required>
          <a-input v-model="form.name" :disabled="editing" placeholder="Bias" />
        </a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="form.status" disabled>
            <a-option value="enabled">enabled</a-option>
            <a-option value="disabled">disabled</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="lookback_periods" label="回看周期数" required>
          <a-input-number v-model="form.lookback_periods" :min="1" />
        </a-form-item>
        <a-form-item class="form-span-2" field="input_columns" label="输入列" required>
          <a-input-tag v-model="inputTags" allow-clear placeholder="输入列名后回车" />
        </a-form-item>
        <a-form-item class="form-span-2" field="outputs" label="输出列" required>
          <a-input-tag v-model="outputTags" :disabled="editing" allow-clear placeholder="输入输出列名后回车" />
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
import { computed, onMounted, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { createFactorDef, deleteFactorDef, listFactorDefs, setFactorStatus, updateFactorDef } from "@/api/factor";
import type { FactorDef } from "@/api/factor/types";
import CodeBlock from "@/components/code-block/index.vue";
import { applyPageResult, defaultPagination, formatTime } from "@/views/data/shared/metadata-utils";
import { validateFactorParamsJSON } from "./factor-form";

defineOptions({ name: "FactorDefinitions" });

const rows = ref<FactorDef[]>([]);
const loading = ref(false);
const visible = ref(false);
const detailVisible = ref(false);
const editing = ref(false);
const selectedFactor = ref<FactorDef | null>(null);
const pagination = reactive(defaultPagination());
const filters = reactive({ status: "" });
const inputTags = ref<string[]>(["close"]);
const outputTags = ref<string[]>(["bias_20"]);

const form = reactive<FactorDef>({
  factor_id: "",
  name: "",
  source_code: "",
  input_columns: ["close"],
  outputs: ["bias_20"],
  params_json: `{"windows":[20]}`,
  lookback_periods: 200,
  status: "disabled"
});

const modalTitle = computed(() => (editing.value ? "编辑因子" : "新增因子"));

async function load() {
  loading.value = true;
  try {
    const rsp = await listFactorDefs({
      status: filters.status || undefined,
      page: { page: pagination.current, size: pagination.pageSize }
    });
    rows.value = rsp.factors || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function reloadFirstPage() {
  pagination.current = 1;
  load();
}

function resetForm() {
  Object.assign(form, {
    factor_id: "",
    name: "",
    source_code: [
      "def compute(df, params):",
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
    status: "disabled"
  });
  inputTags.value = ["close"];
  outputTags.value = ["bias_20"];
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openDetail(record: FactorDef) {
  selectedFactor.value = record;
  detailVisible.value = true;
}

function openEdit(record: FactorDef) {
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
  if (editing.value) await updateFactorDef(payload);
  else await createFactorDef(payload);
  Message.success("因子已保存");
  visible.value = false;
  await load();
}

async function toggleStatus(record: FactorDef) {
  const next = record.status === "enabled" ? "disabled" : "enabled";
  await setFactorStatus(record.factor_id, next);
  Message.success("状态已更新");
  await load();
}

async function remove(record: FactorDef) {
  try {
    await deleteFactorDef(record.factor_id);
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

onMounted(load);
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

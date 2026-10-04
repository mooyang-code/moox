<template>
  <a-modal
    v-model:visible="visible"
    width="920px"
    :title="editing ? '编辑因子' : '新增因子'"
    :align-center="false"
    top="40px"
    :modal-style="{ maxWidth: 'calc(100vw - 32px)' }"
    :body-style="{ maxHeight: 'calc(100vh - 168px)', overflowY: 'auto', padding: '18px var(--moox-space-6) 14px' }"
    :ok-loading="submitting"
    :on-before-ok="submit"
    @before-open="prepare"
  >
    <a-alert v-if="editing" type="info" show-icon class="editor-tip"
      >编辑只能在停用状态进行：停用 → 修改 → 启用，启用时会自动回填历史。</a-alert
    >
    <a-alert v-else type="info" show-icon class="editor-tip">新建的因子处于停用状态，启用后才开始计算并回填历史。</a-alert>
    <a-form class="factor-form" :model="form" layout="vertical">
      <a-form-item field="factor_id" label="因子 ID" required>
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
      <a-form-item field="lookback_periods" label="回看周期数" required>
        <a-input-number v-model="form.lookback_periods" :min="1" />
      </a-form-item>
      <a-form-item
        v-if="form.factor_type === 'cross_section'"
        class="form-span-2"
        field="allow_partial_universe"
        label="截面计算"
      >
        <a-checkbox v-model="form.allow_partial_universe">允许部分对象参与计算（上游缺失对象时不跳过整个截面）</a-checkbox>
      </a-form-item>
      <a-form-item
        class="form-span-2"
        field="input_columns"
        label="输入列"
        required
        :validate-status="inputError ? 'error' : undefined"
        :help="inputError"
      >
        <a-select
          v-model="inputColumns"
          multiple
          allow-search
          allow-clear
          :loading="columnsLoading"
          placeholder="从源数据集的列中选择"
        >
          <a-option v-for="name in sourceColumns" :key="name" :value="name">{{ name }}</a-option>
        </a-select>
      </a-form-item>
      <a-form-item
        class="form-span-2"
        field="outputs"
        label="输出列"
        required
        :validate-status="outputError ? 'error' : undefined"
        :help="outputError || '不得与源数据集列、保留列、同因子集其他因子的输出重名'"
      >
        <a-input-tag v-model="outputs" allow-clear placeholder="输入输出列名后回车" />
      </a-form-item>
      <a-form-item class="form-span-2" field="params_json" label="参数 JSON" required>
        <a-textarea v-model="form.params_json" :auto-size="{ minRows: 4, maxRows: 10 }" />
      </a-form-item>
      <a-form-item class="form-span-2" field="source_code" label="源码" required extra="源码按 hash 不可变，修改后会生成新版本。">
        <a-textarea v-model="form.source_code" class="code-editor" :auto-size="{ minRows: 16, maxRows: 28 }" />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { createFactor, updateFactor } from "@/api/factor";
import type { FactorDef, FactorSet } from "@/api/factor/types";
import { listDatasetColumns } from "@/api/storage/metadata";
import type { DatasetColumn } from "@/api/storage/types";
import { checkInputs, checkOutputs, FACTOR_SOURCE_TEMPLATE, sourceInputColumns, validateFactorParamsJSON } from "./factor-form";

defineOptions({ name: "FactorEditor" });

const props = defineProps<{ set: FactorSet; siblings: FactorDef[]; factor: FactorDef | null }>();
const emit = defineEmits<{ (e: "saved", factor: FactorDef): void }>();
const visible = defineModel<boolean>("visible", { default: false });

const editing = computed(() => props.factor !== null);
const submitting = ref(false);
const columnsLoading = ref(false);
const sourceColumns = ref<string[]>([]);
const inputColumns = ref<string[]>([]);
const outputs = ref<string[]>([]);
const touched = ref(false);
const form = reactive<FactorDef>(blankFactor());

function blankFactor(): FactorDef {
  return {
    factor_id: "",
    set_id: "",
    factor_type: "timeseries",
    name: "",
    source_code: FACTOR_SOURCE_TEMPLATE,
    input_columns: [],
    outputs: [],
    params_json: `{"windows":[20]}`,
    lookback_periods: 200,
    allow_partial_universe: false,
    status: "disabled"
  };
}

const siblingOutputs = computed(() =>
  props.siblings.filter(item => item.factor_id !== form.factor_id).flatMap(item => item.outputs || [])
);
const inputError = computed(() => (touched.value ? checkInputs(inputColumns.value, sourceColumns.value) : ""));
const outputError = computed(() =>
  touched.value ? checkOutputs(outputs.value, { sourceColumns: sourceColumns.value, siblingOutputs: siblingOutputs.value }) : ""
);

async function loadSourceColumns() {
  columnsLoading.value = true;
  try {
    const columns: DatasetColumn[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listDatasetColumns({
        space_id: props.set.space_id,
        dataset_id: props.set.source_dataset_id,
        page: { page, size: 500 }
      });
      columns.push(...(rsp.columns || []));
      if (!rsp.page_result?.has_more || !(rsp.columns || []).length) break;
    }
    sourceColumns.value = sourceInputColumns(columns);
  } catch (error) {
    sourceColumns.value = [];
    Message.error(error instanceof Error ? error.message : "源数据集列加载失败");
  } finally {
    columnsLoading.value = false;
  }
}

async function prepare() {
  touched.value = false;
  Object.assign(form, props.factor ? { ...props.factor } : { ...blankFactor(), set_id: props.set.set_id });
  inputColumns.value = [...(form.input_columns || [])];
  outputs.value = [...(form.outputs || [])];
  await loadSourceColumns();
}

async function submit() {
  touched.value = true;
  if (!form.factor_id.trim() || !form.name.trim() || !form.source_code.trim()) {
    Message.warning("请补全因子 ID、模块名和源码");
    return false;
  }
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(form.factor_id)) {
    Message.warning("因子 ID 必须以字母或下划线开头，仅含字母数字下划线");
    return false;
  }
  if (inputError.value || outputError.value) return false;
  let paramsJSON: string;
  try {
    paramsJSON = validateFactorParamsJSON(form.params_json);
  } catch (error) {
    Message.warning(error instanceof SyntaxError ? "参数必须是合法 JSON" : "参数必须是 JSON object");
    return false;
  }
  const payload: FactorDef = {
    ...form,
    set_id: props.set.set_id,
    input_columns: [...inputColumns.value],
    outputs: [...outputs.value],
    params_json: paramsJSON,
    allow_partial_universe: form.factor_type === "cross_section" ? Boolean(form.allow_partial_universe) : false,
    status: "disabled"
  };
  submitting.value = true;
  try {
    const saved = editing.value ? await updateFactor(payload) : await createFactor(payload);
    Message.success(editing.value ? "因子已保存" : "因子已创建（停用状态）");
    emit("saved", saved);
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "保存因子失败");
    return false;
  } finally {
    submitting.value = false;
  }
}
</script>

<style scoped>
.editor-tip {
  margin-bottom: var(--moox-space-3);
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

@media (max-width: 768px) {
  .factor-form {
    grid-template-columns: 1fr;
  }

  .form-span-2 {
    grid-column: span 1;
  }
}
</style>

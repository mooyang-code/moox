<template>
  <a-modal
    v-model:visible="visible"
    :title="`添加因子到「${title}」`"
    :width="760"
    :mask-closable="!submitting"
    unmount-on-close
    @before-open="prepare"
  >
    <div class="add-factor__toolbar">
      <a-input-search v-model="keyword" allow-clear placeholder="搜索因子 ID 或模块名" class="add-factor__search" />
      <span class="add-factor__count">已选 {{ selected.length }} 个</span>
    </div>
    <a-alert v-if="loadError" type="error" show-icon class="add-factor__alert">{{ loadError }}</a-alert>
    <a-spin :loading="loading" class="add-factor__spin">
      <a-empty v-if="!rows.length && !loading" description="因子定义库里没有可选的因子，请先到「因子定义」新增" />
      <div v-else class="add-factor__list" role="group" aria-label="候选因子">
        <label v-for="row in rows" :key="row.factor.factor_id" class="add-factor__row" :class="{ 'is-disabled': !row.check.ok }">
          <a-checkbox
            :model-value="selected.includes(row.factor.factor_id)"
            :disabled="!row.check.ok || submitting"
            @change="toggle(row.factor.factor_id, $event)"
          />
          <span class="add-factor__main">
            <span class="add-factor__name">
              <strong>{{ row.factor.factor_id }}</strong>
              <a-tag size="small">{{ factorTypeLabel(row.factor.factor_type) }}</a-tag>
            </span>
            <span class="add-factor__io"
              >输入 {{ row.factor.input_columns.join(", ") }} · 输出 {{ row.factor.outputs.join(", ") }}</span
            >
          </span>
          <span class="add-factor__note" :class="noteClass(row)">{{ noteText(row) }}</span>
        </label>
      </div>
    </a-spin>
    <template #footer>
      <a-space>
        <a-button :disabled="submitting" @click="visible = false">取消</a-button>
        <a-button type="primary" status="success" :loading="submitting" :disabled="!selected.length" @click="submit">
          添加（{{ selected.length }}）
        </a-button>
      </a-space>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { addFactorToSet, listFactors } from "@/api/factor";
import type { FactorDef, FactorSetInfo } from "@/api/factor/types";
import { listDatasetColumns } from "@/api/storage/metadata";
import { factorTypeLabel } from "@/views/factor/shared/status";
import { precheckAddFactor, type PrecheckResult } from "./add-factor-precheck";

defineOptions({ name: "FactorAddFactorModal" });

const props = defineProps<{ info: FactorSetInfo; title: string }>();
const emit = defineEmits<{ (e: "added", factorIds: string[]): void }>();
const visible = defineModel<boolean>("visible", { default: false });

const PAGE_SIZE = 500;

const factors = ref<FactorDef[]>([]);
const sourceColumns = ref<string[] | null>(null);
const keyword = ref("");
const selected = ref<string[]>([]);
const added = ref<string[]>([]);
const errors = ref<Record<string, string>>({});
const loading = ref(false);
const loadError = ref("");
const submitting = ref(false);

interface CandidateRow {
  factor: FactorDef;
  check: PrecheckResult;
}

const memberDefs = computed(() => props.info.members.map(member => member.factor));

const rows = computed<CandidateRow[]>(() => {
  const text = keyword.value.trim().toLowerCase();
  const members = [...memberDefs.value, ...factors.value.filter(item => added.value.includes(item.factor_id))];
  return factors.value
    .filter(item => !text || item.factor_id.toLowerCase().includes(text) || item.name.toLowerCase().includes(text))
    .map(item => ({ factor: item, check: precheckAddFactor({ candidate: item, members, sourceColumns: sourceColumns.value }) }));
});

async function fetchAllFactors() {
  const items: FactorDef[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listFactors({ page: { page, size: PAGE_SIZE } });
    const batch = rsp.factors || [];
    items.push(...batch.map(item => item.factor));
    if (!rsp.page_result?.has_more || !batch.length) break;
  }
  return items;
}

async function fetchSourceColumns(spaceId: string, datasetId: string) {
  const names: string[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listDatasetColumns({ space_id: spaceId, dataset_id: datasetId, page: { page, size: PAGE_SIZE } });
    const batch = rsp.columns || [];
    names.push(...batch.map(column => column.column_name));
    if (!rsp.page_result?.has_more || !batch.length) break;
  }
  return names;
}

async function prepare() {
  keyword.value = "";
  selected.value = [];
  added.value = [];
  errors.value = {};
  loadError.value = "";
  sourceColumns.value = null;
  loading.value = true;
  const { space_id: spaceId, source_dataset_id: datasetId } = props.info.factor_set;
  try {
    const [items, columns] = await Promise.all([fetchAllFactors(), fetchSourceColumns(spaceId, datasetId).catch(() => null)]);
    factors.value = items;
    sourceColumns.value = columns;
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : "因子定义加载失败";
    factors.value = [];
  } finally {
    loading.value = false;
  }
}

function toggle(factorId: string, checked: boolean | (string | number | boolean)[]) {
  const on = Array.isArray(checked) ? checked.length > 0 : checked;
  selected.value = on ? [...new Set([...selected.value, factorId])] : selected.value.filter(id => id !== factorId);
}

function noteText(row: CandidateRow) {
  const failure = errors.value[row.factor.factor_id];
  if (failure) return failure;
  return row.check.ok ? "可添加" : row.check.reason;
}

function noteClass(row: CandidateRow) {
  if (errors.value[row.factor.factor_id]) return "is-error";
  if (!row.check.ok) return row.check.reason === "已在该计算任务中" ? "is-muted" : "is-error";
  return "is-ok";
}

/** 逐个提交；预检只是提示，失败按行展示后端给出的原因。 */
async function submit() {
  const { set_id: setId } = props.info.factor_set;
  submitting.value = true;
  errors.value = {};
  const done: string[] = [];
  try {
    for (const factorId of [...selected.value]) {
      try {
        await addFactorToSet(setId, factorId);
        done.push(factorId);
      } catch (error) {
        errors.value = { ...errors.value, [factorId]: error instanceof Error ? error.message : "添加失败" };
      }
    }
  } finally {
    submitting.value = false;
  }
  if (done.length) {
    added.value = [...added.value, ...done];
    selected.value = selected.value.filter(id => !done.includes(id));
    emit("added", done);
  }
  if (!Object.keys(errors.value).length) {
    Message.success(`已添加 ${done.length} 个因子（已停用），在成员列表里启用后才会计算`);
    visible.value = false;
  } else {
    Message.warning(`${done.length} 个已添加，${Object.keys(errors.value).length} 个失败，原因见对应行`);
  }
}
</script>

<style scoped>
.add-factor__toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-3);
}

.add-factor__search {
  max-width: 280px;
}

.add-factor__count {
  color: var(--color-text-3);
  font-size: 12px;
}

.add-factor__alert {
  margin-bottom: var(--moox-space-3);
}

.add-factor__spin {
  display: block;
  width: 100%;
}

.add-factor__list {
  display: flex;
  max-height: 420px;
  flex-direction: column;
  overflow-y: auto;
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
}

.add-factor__row {
  display: flex;
  align-items: center;
  gap: var(--moox-space-3);
  padding: 10px 12px;
  border-bottom: 1px solid var(--color-border-2);
  cursor: pointer;
}

.add-factor__row:last-child {
  border-bottom: 0;
}

.add-factor__row.is-disabled {
  background: var(--color-fill-1);
  cursor: not-allowed;
}

.add-factor__main {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  gap: 2px;
}

.add-factor__name {
  display: flex;
  align-items: center;
  gap: var(--moox-space-2);
}

.add-factor__io {
  overflow: hidden;
  color: var(--color-text-3);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.add-factor__note {
  max-width: 240px;
  font-size: 12px;
  text-align: right;
}

.add-factor__note.is-ok {
  color: rgb(var(--success-6));
}

.add-factor__note.is-muted {
  color: var(--color-text-3);
}

.add-factor__note.is-error {
  color: rgb(var(--danger-6));
}
</style>

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
        <div v-for="row in rows" :key="row.factor.factor_id" class="add-factor__item">
          <label class="add-factor__row" :class="{ 'is-disabled': row.locked }">
            <a-checkbox
              :model-value="selected.includes(row.factor.factor_id)"
              :disabled="(row.locked && !selected.includes(row.factor.factor_id)) || submitting"
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
          <div v-if="selected.includes(row.factor.factor_id)" class="add-factor__config">
            <span v-for="decl in row.decls" :key="decl.name" class="add-factor__param">
              <span class="add-factor__param-name">{{ decl.name }}</span>
              <a-input-number
                :model-value="paramValue(row.factor.factor_id, decl)"
                :min="decl.min"
                :max="decl.max"
                :precision="decl.type === 'int' ? 0 : undefined"
                :disabled="submitting"
                size="small"
                hide-button
                class="add-factor__param-input"
                @change="setParam(row.factor.factor_id, decl.name, $event)"
              />
              <span class="add-factor__param-hint">{{ decl.min }} ~ {{ decl.max }}，默认 {{ decl.default_value }}</span>
            </span>
            <span class="add-factor__param">
              <span class="add-factor__param-name">成员类型</span>
              <a-radio-group
                :model-value="kindOf(row.factor.factor_id)"
                type="button"
                size="small"
                :disabled="submitting"
                @change="setKind(row.factor.factor_id, $event as MemberKind)"
              >
                <a-radio value="fixed">固定参数</a-radio>
                <a-radio value="var" :disabled="!row.decls.length">可变（策略逐期提交）</a-radio>
              </a-radio-group>
              <span v-if="kindOf(row.factor.factor_id) === 'var'" class="add-factor__param-hint"
                >上面的参数是默认参数，加成员之后不能改</span
              >
            </span>
            <span v-if="row.check.ok" class="add-factor__preview"
              >成员键 {{ row.check.memberKey }} · 结果列 {{ row.check.columns.join(", ") }}</span
            >
          </div>
        </div>
      </div>
    </a-spin>
    <template #footer>
      <a-space>
        <a-button :disabled="submitting" @click="visible = false">取消</a-button>
        <a-button type="primary" status="success" :loading="submitting" :disabled="!canSubmit" @click="submit">
          添加（{{ selected.length }}）
        </a-button>
      </a-space>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { createSubmitScope } from "@/utils/submit-scope";
import { computed, ref, watch, onBeforeUnmount } from "vue";
import { Message } from "@arco-design/web-vue";
import { addFactorToTask, listFactors } from "@/api/factor";
import type { FactorDef, FactorParamDecl, FactorTaskInfo, MemberKind } from "@/api/factor/types";
import { listDatasetColumns } from "@/api/storage/metadata";
import { selectedSpaceGeneration } from "@/api/admin/space-header";
import { useSpaceStore } from "@/store/modules/space";
import { factorTypeLabel } from "@/views/factor/shared/status";
import { precheckAddFactor, type PrecheckMember, type PrecheckResult } from "./add-factor-precheck";

defineOptions({ name: "FactorAddFactorModal" });

const props = defineProps<{ info: FactorTaskInfo; title: string }>();
const emit = defineEmits<{ (e: "added", factorIds: string[]): void }>();
const visible = defineModel<boolean>("visible", { default: false });

const PAGE_SIZE = 500;

const factors = ref<FactorDef[]>([]);
const sourceColumns = ref<string[] | null>(null);
const keyword = ref("");
const selected = ref<string[]>([]);
const errors = ref<Record<string, string>>({});
const loading = ref(false);
const loadError = ref("");
const submitting = ref(false);

interface CandidateRow {
  factor: FactorDef;
  decls: FactorParamDecl[];
  check: PrecheckResult;
  /** 这个因子本身加不进去（不是改参数或别名能解决的）：行置灰，不能选。 */
  locked: boolean;
}

/** 每个选中因子的草稿：参数（只存改过的）与成员类型。 */
const drafts = ref<Record<string, { params: Record<string, number>; kind: MemberKind }>>({});
/** 本次弹窗里已经加入的成员（成员键、类型与参数），预检把它们算作已有成员。 */
const addedMembers = ref<PrecheckMember[]>([]);

const memberViews = computed<PrecheckMember[]>(() => [
  ...props.info.members.map(member => ({
    factor_id: member.factor_id,
    member_key: member.member_key,
    kind: member.kind,
    params: member.params,
    output_columns: member.output_columns?.length ? member.output_columns : member.factor.outputs
  })),
  ...addedMembers.value
]);

const rows = computed<CandidateRow[]>(() => {
  const text = keyword.value.trim().toLowerCase();
  return factors.value
    .filter(item => !text || item.factor_id.toLowerCase().includes(text) || item.name.toLowerCase().includes(text))
    .map(item => {
      const draft = drafts.value[item.factor_id];
      const check = precheckAddFactor({
        candidate: item,
        members: memberViews.value,
        sourceColumns: sourceColumns.value,
        kind: selected.value.includes(item.factor_id) ? draft?.kind : undefined,
        params: selected.value.includes(item.factor_id) ? draft?.params : undefined
      });
      return { factor: item, decls: item.params ?? [], check, locked: !check.ok && !check.fixable };
    });
});

const canSubmit = computed(
  () => selected.value.length > 0 && selected.value.every(id => rows.value.find(row => row.factor.factor_id === id)?.check.ok)
);

function paramValue(factorId: string, decl: FactorParamDecl) {
  return drafts.value[factorId]?.params[decl.name] ?? decl.default_value;
}

function draftOf(factorId: string) {
  return (drafts.value[factorId] ??= { params: {}, kind: "fixed" });
}

function kindOf(factorId: string): MemberKind {
  return drafts.value[factorId]?.kind ?? "fixed";
}

function setKind(factorId: string, kind: MemberKind) {
  draftOf(factorId).kind = kind;
}

function setParam(factorId: string, name: string, value: number | undefined) {
  const draft = draftOf(factorId);
  if (value === undefined || value === null || Number.isNaN(value)) delete draft.params[name];
  else draft.params[name] = value;
}

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
  addedMembers.value = [];
  drafts.value = {};
  errors.value = {};
  loadError.value = "";
  sourceColumns.value = null;
  loading.value = true;
  const { space_id: spaceId, source_dataset_id: datasetId } = props.info.factor_task;
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
  return row.check.ok
    ? row.check.memberKey === row.factor.factor_id
      ? "可添加"
      : `可添加，成员键 ${row.check.memberKey}`
    : row.check.reason;
}

function noteClass(row: CandidateRow) {
  if (errors.value[row.factor.factor_id]) return "is-error";
  if (!row.check.ok) return row.check.reason === "已在该计算任务中" ? "is-muted" : "is-error";
  return "is-ok";
}

/** 逐个提交；预检只是提示，失败按行展示后端给出的原因。 */
const spaceStore = useSpaceStore();

async function submit() {
  const { task_id: taskId } = props.info.factor_task;
  // 批量添加固定发起空间与代次：每一项请求都按当前空间头发出，空间切换后继续提交会把旧任务的因子写到新空间
  const submitSpaceId = spaceStore.selectedSpaceId;
  const submitGeneration = selectedSpaceGeneration();
  const submission = addScope.begin();
  const stale = () =>
    !submission.isCurrent() || spaceStore.selectedSpaceId !== submitSpaceId || selectedSpaceGeneration() !== submitGeneration;
  submitting.value = true;
  errors.value = {};
  const done: string[] = [];
  try {
    for (const factorId of [...selected.value]) {
      if (stale()) break;
      try {
        const draft = drafts.value[factorId];
        const row = rows.value.find(item => item.factor.factor_id === factorId);
        const decls = row?.decls ?? [];
        // 有参数的因子把完整参数一起提交（没改的取默认值）；成员键由服务端按参数推导并返回。
        const params = decls.length
          ? Object.fromEntries(decls.map(decl => [decl.name, draft?.params[decl.name] ?? decl.default_value]))
          : undefined;
        const kind = draft?.kind ?? "fixed";
        const member = await addFactorToTask(taskId, factorId, kind, params);
        addedMembers.value = [
          ...addedMembers.value,
          {
            factor_id: factorId,
            member_key: member.member_key,
            kind: member.kind ?? kind,
            params: member.params ?? params,
            output_columns: member.output_columns ?? []
          }
        ];
        done.push(factorId);
      } catch (error) {
        if (stale()) break; // 弹窗已被关闭/重新打开：失败原因不能写进新弹窗的行
        errors.value = { ...errors.value, [factorId]: error instanceof Error ? error.message : "添加失败" };
      }
    }
  } finally {
    if (!stale()) submitting.value = false;
  }
  if (stale()) {
    // 弹窗已被关闭/重新打开或空间已切换：已添加的因子保留，其余未提交；不能改动新弹窗的状态、发出 added 或关闭它
    if (done.length) Message.info(`弹窗已关闭或空间已切换：已向原计算任务添加 ${done.length} 个因子，其余未提交`);
    return;
  }
  if (done.length) {
    selected.value = selected.value.filter(id => !done.includes(id));
    for (const id of done) delete drafts.value[id];
    emit("added", done);
  }
  if (!Object.keys(errors.value).length) {
    Message.success(`已添加 ${done.length} 个因子（已停用），在成员列表里启用后才会计算`);
    visible.value = false;
  } else {
    Message.warning(`${done.length} 个已添加，${Object.keys(errors.value).length} 个失败，原因见对应行`);
  }
}

// 提交作用域：右上角关闭按钮/Esc 也能关闭弹窗，打开、关闭、卸载都使进行中的批量添加失效，新一轮打开重置提交状态
const addScope = createSubmitScope();
watch(visible, () => {
  addScope.invalidate();
  submitting.value = false;
});
onBeforeUnmount(() => addScope.invalidate());
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

.add-factor__item {
  border-bottom: 1px solid var(--color-border-2);
}

.add-factor__item:last-child {
  border-bottom: 0;
}

.add-factor__config {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-3) var(--moox-space-4);
  padding: 0 12px 10px 44px;
}

.add-factor__param {
  display: inline-flex;
  align-items: center;
  gap: var(--moox-space-2);
}

.add-factor__param-name {
  color: var(--color-text-2);
  font-size: 12px;
}

.add-factor__param-input {
  width: 96px;
}

.add-factor__preview {
  width: 100%;
  color: var(--color-text-3);
  font-size: 12px;
}

.add-factor__param-hint {
  color: var(--color-text-3);
  font-size: 12px;
}

.add-factor__row {
  display: flex;
  align-items: center;
  gap: var(--moox-space-3);
  padding: 10px 12px;
  cursor: pointer;
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

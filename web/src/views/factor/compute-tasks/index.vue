<template>
  <div class="moox-page compute-tasks-page">
    <div class="moox-inner">
      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <a-alert v-if="store.loadError" type="error" show-icon class="tasks-alert">
          {{ store.loadError }}
          <template #action><a-button size="mini" @click="reload">重试</a-button></template>
        </a-alert>

        <div class="result-toolbar">
          <span class="result-count">
            共 {{ filteredSets.length }} 个计算任务
            <InfoTip text="一个计算任务 = 一个源数据集 × 一个频率，持续计算一组因子，写入一个结果数据集和默认视图。" />
          </span>
          <a-space wrap>
            <a-input-search v-model="filters.keyword" allow-clear placeholder="搜索计算任务或源数据集" class="tasks-search" />
            <a-select v-model="filters.status" allow-clear placeholder="全部状态" class="tasks-status">
              <a-option v-for="option in statusOptions" :key="option.value" :value="option.value">{{ option.label }}</a-option>
            </a-select>
            <a-button type="primary" status="success" @click="createVisible = true">
              <template #icon><icon-plus /></template>
              新建计算任务
            </a-button>
          </a-space>
        </div>

        <a-table
          row-key="set_id"
          size="small"
          :bordered="{ cell: true }"
          :data="rows"
          :loading="store.loading && !store.sets.length"
          :pagination="false"
          :scroll="{ x: 'max-content' }"
        >
          <template #empty>
            <a-empty description="当前空间没有计算任务" />
          </template>
          <template #columns>
            <a-table-column title="计算任务" :width="240">
              <template #cell="{ record }">
                <a-link @click="openDetail(record.set_id)">{{ record.label }}</a-link>
                <div class="cell-sub">{{ record.set_id }}</div>
              </template>
            </a-table-column>
            <a-table-column title="源数据集" :width="200" :ellipsis="true" :tooltip="true">
              <template #cell="{ record }">{{ record.sourceName }}</template>
            </a-table-column>
            <a-table-column title="频率" data-index="freq" :width="80" />
            <a-table-column title="对象范围" :width="110">
              <template #cell="{ record }">{{ record.scope }}</template>
            </a-table-column>
            <a-table-column title="因子" :width="90" align="center">
              <template #cell="{ record }">{{ record.members }}</template>
            </a-table-column>
            <a-table-column title="状态" :width="90" align="center">
              <template #cell="{ record }">
                <a-tag size="small" :color="record.statusTag.color">{{ record.statusTag.label }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="周期健康" :width="100" align="center">
              <template #cell="{ record }">
                <HealthTag :set="record.info.factor_set" :last-run="record.info.last_run" />
              </template>
            </a-table-column>
            <a-table-column title="最近周期" :width="190">
              <template #cell="{ record }">
                <span>{{ record.lastPeriod }}</span>
                <div v-if="record.lag" class="cell-sub">滞后 {{ record.lag }}</div>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="230" align="center" fixed="right">
              <template #cell="{ record }">
                <a-space wrap>
                  <a-button size="mini" type="text" @click="openDetail(record.set_id)">详情</a-button>
                  <a-button
                    v-if="record.action === 'retry'"
                    size="mini"
                    type="text"
                    :loading="busy"
                    @click="retryActivation(record.info.factor_set)"
                  >
                    重试激活
                  </a-button>
                  <a-popconfirm
                    v-if="record.action === 'disable'"
                    content="停用后不再接收周期事件；结果数据集和 View 保留，确认停用？"
                    @ok="toggleStatus(record.info.factor_set, 'disabled')"
                  >
                    <a-button size="mini" type="text" status="danger" :loading="busy">停用</a-button>
                  </a-popconfirm>
                  <a-popconfirm
                    v-if="record.action === 'enable'"
                    :content="ENABLE_ENGINE_HINT"
                    @ok="toggleStatus(record.info.factor_set, 'enabled')"
                  >
                    <a-button size="mini" type="text" status="success" :loading="busy">启用</a-button>
                  </a-popconfirm>
                  <a-tooltip :content="record.remove.reason" :disabled="!record.remove.disabled">
                    <span>
                      <a-button
                        size="mini"
                        type="text"
                        status="danger"
                        :disabled="record.remove.disabled"
                        @click="openDelete(record.info.factor_set)"
                      >
                        删除
                      </a-button>
                    </span>
                  </a-tooltip>
                </a-space>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </template>
    </div>

    <SetCreateModal v-model:visible="createVisible" @created="onCreated" />

    <TaskDetailDrawer
      :info="detailInfo"
      :busy="busy"
      @close="closeDetail"
      @retry="retryActivation"
      @toggle-status="toggleStatus"
      @edit-scope="openScope"
      @delete="openDelete"
    />

    <a-modal v-model:visible="scopeVisible" title="修改对象范围" :width="560" :ok-loading="busy" :on-before-ok="saveScope">
      <a-alert type="info" show-icon class="dialog-tip">从下一个周期起生效；已写入的历史需要手动补算。</a-alert>
      <a-form layout="vertical">
        <SubjectScopeFields v-model:mode="scopeMode" v-model:subjects="scopeSubjects" />
      </a-form>
    </a-modal>

    <a-modal
      v-model:visible="deleteVisible"
      title="删除计算任务"
      :width="560"
      :ok-loading="busy"
      :ok-button-props="{ status: 'danger', disabled: !deleteConfirmed }"
      :on-before-ok="confirmDelete"
    >
      <a-radio-group v-model="purge" direction="vertical">
        <a-radio :value="false">仅删除计算任务（保留结果数据集和已写入的数据）</a-radio>
        <a-radio :value="true">同时清理结果数据集（purge，不可恢复）</a-radio>
      </a-radio-group>
      <a-form-item v-if="purge" class="delete-confirm" :label="`请输入 ${target?.set_id} 以确认`">
        <a-input v-model="confirmText" allow-clear />
      </a-form-item>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { useRoute } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { createFactorSet, deleteFactorSet, setFactorSetStatus, updateFactorSet } from "@/api/factor";
import type { FactorSet, FactorSetInfo, SubjectMode } from "@/api/factor/types";
import { formatLag, formatPeriod } from "@/views/factor/shared/health";
import HealthTag from "@/views/factor/shared/health-tag.vue";
import InfoTip from "@/views/factor/shared/info-tip.vue";
import { setStatusTag } from "@/views/factor/shared/status";
import { useFactorScope } from "@/views/factor/shared/use-factor-scope";
import {
  deleteState,
  ENABLE_ENGINE_HINT,
  filterSets,
  memberCountText,
  scopeText,
  setActionKind,
  type TaskFilters
} from "./compute-tasks-model";
import SetCreateModal from "./set-create-modal.vue";
import SubjectScopeFields from "./subject-scope-fields.vue";
import TaskDetailDrawer from "./task-detail-drawer.vue";

defineOptions({ name: "FactorComputeTasks" });

const route = useRoute();
const { store, spaceId, navigate, reload } = useFactorScope();

const statusOptions = [
  { value: "enabled", label: "运行中" },
  { value: "pending", label: "创建中" },
  { value: "disabled", label: "已停用" },
  { value: "deleting", label: "清理中" }
] as const;

const filters = reactive<TaskFilters>({ keyword: "", status: "" });
const createVisible = ref(false);
const busy = ref(false);

const filteredSets = computed(() => filterSets(store.sets, filters, (set: FactorSet) => store.setLabel(set)));

const rows = computed(() =>
  filteredSets.value.map((info: FactorSetInfo) => ({
    set_id: info.factor_set.set_id,
    info,
    label: store.setLabel(info.factor_set),
    sourceName: store.datasetNames[info.factor_set.source_dataset_id] || info.factor_set.source_dataset_id,
    freq: info.factor_set.freq,
    scope: scopeText(info.factor_set),
    members: memberCountText(info),
    statusTag: setStatusTag(info.factor_set.status),
    action: setActionKind(info.factor_set),
    remove: deleteState(info),
    lastPeriod: info.last_run?.last_period_time ? formatPeriod(info.last_run.last_period_time) : "-",
    lag: info.last_run?.last_period_time ? formatLag(info.last_run.lag_seconds) : ""
  }))
);

const detailId = computed(() => {
  const value = route.query.detail;
  return typeof value === "string" ? value : "";
});
const detailInfo = computed<FactorSetInfo | undefined>(() =>
  store.sets.find((item: FactorSetInfo) => item.factor_set.set_id === detailId.value)
);

function openDetail(setId: string) {
  void navigate({ detail: setId });
}

function closeDetail() {
  void navigate({ detail: undefined });
}

async function onCreated(setId: string) {
  await store.reload({ silent: false });
  openDetail(setId);
}

async function run(action: () => Promise<unknown>, success: string, failure: string) {
  busy.value = true;
  try {
    await action();
    Message.success(success);
    await store.reload();
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : failure);
    return false;
  } finally {
    busy.value = false;
  }
}

function toggleStatus(set: FactorSet, next: "enabled" | "disabled") {
  return run(
    () => setFactorSetStatus(set.set_id, next),
    next === "enabled" ? "计算任务已启用" : "计算任务已停用",
    "更新计算任务状态失败"
  );
}

function retryActivation(set: FactorSet) {
  return run(
    () =>
      createFactorSet({
        set_id: set.set_id,
        space_id: set.space_id,
        source_dataset_id: set.source_dataset_id,
        freq: set.freq,
        result_dataset_id: set.result_dataset_id,
        subject_mode: set.subject_mode,
        subjects: set.subjects
      }),
    "已重新提交激活",
    "重试激活失败"
  );
}

const target = ref<FactorSet | null>(null);
const scopeVisible = ref(false);
const scopeMode = ref<SubjectMode>("all");
const scopeSubjects = ref<string[]>([]);

function openScope(set: FactorSet) {
  target.value = set;
  scopeMode.value = set.subject_mode;
  scopeSubjects.value = [...(set.subjects || [])];
  scopeVisible.value = true;
}

async function saveScope() {
  const current = target.value;
  if (!current) return false;
  if (scopeMode.value === "include" && !scopeSubjects.value.length) {
    Message.warning("指定对象模式至少需要一个对象 ID");
    return false;
  }
  return run(
    () => updateFactorSet(current.set_id, scopeMode.value, scopeMode.value === "include" ? [...scopeSubjects.value] : []),
    "对象范围已更新",
    "更新对象范围失败"
  );
}

const deleteVisible = ref(false);
const purge = ref(false);
const confirmText = ref("");
const deleteConfirmed = computed(() => !purge.value || confirmText.value.trim() === target.value?.set_id);

function openDelete(set: FactorSet) {
  target.value = set;
  purge.value = false;
  confirmText.value = "";
  deleteVisible.value = true;
}

async function confirmDelete() {
  const current = target.value;
  if (!current || !deleteConfirmed.value) return false;
  const ok = await run(
    () => deleteFactorSet(current.set_id, purge.value),
    purge.value ? "计算任务已删除，结果数据集清理中" : "计算任务已删除",
    "删除计算任务失败"
  );
  if (ok && detailId.value === current.set_id) closeDetail();
  return ok;
}
</script>

<style scoped lang="scss">
@use "../shared/factor-page.scss";

.compute-tasks-page {
  min-width: 0;
}

.tasks-alert,
.dialog-tip {
  margin-bottom: var(--moox-space-3);
}

.tasks-search {
  width: 240px;
}

.tasks-status {
  width: 130px;
}

.cell-sub {
  color: var(--color-text-3);
  font-size: 12px;
}

.delete-confirm {
  margin-top: var(--moox-space-3);
}
</style>

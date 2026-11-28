<template>
  <div class="moox-page factor-results-page">
    <div class="moox-inner">
      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <a-alert v-if="store.loadError" type="error" show-icon class="results-alert">
          {{ store.loadError }}
          <template #action><a-button size="mini" @click="reload">重试</a-button></template>
        </a-alert>

        <a-empty v-if="!available.length && !store.loading && !store.loadError" description="暂无计算任务">
          <template #extra>
            <a-button @click="goTasks">前往计算任务</a-button>
          </template>
        </a-empty>

        <template v-else-if="available.length">
          <div class="result-toolbar">
            <TaskTabs :model-value="activeTaskId" :include="isResultTask" class="result-tabs" @change="onTabChange" />
            <a-space class="result-actions">
              <span v-if="readAt" class="result-read-at">任务状态读取于 {{ readAt }}</span>
              <a-button :loading="resultLoading" @click="refreshResults">
                <template #icon><icon-refresh /></template>
                刷新结果
              </a-button>
            </a-space>
          </div>

          <a-alert v-if="scopeError" type="error" show-icon class="results-alert">
            {{ scopeError }}
            <template #action><a-button size="mini" @click="loadResultView">重试</a-button></template>
          </a-alert>

          <ViewBrowse
            v-if="resultView"
            :key="`${resultView.space_id}/${resultView.dataset_id}`"
            :zone="displayZone"
            :dataset-ids="[resultView.dataset_id]"
            :active-dataset-id="resultView.dataset_id"
            :refresh-key="refreshKey"
            :query-ready="scopeReady"
            :display-columns="table.display"
            :project-columns="table.project"
            :search-columns="table.search"
            :extra-filters="extraFilters"
            row-extra-title="因子"
            :auto-refresh-interval-ms="60000"
            empty-description="结果数据集准备中"
            empty-rows-description="计算任务已准备，尚未产生数据"
            @cleared="setConditions([])"
          >
            <template #status-extra>
              <a-tag v-if="summary.hasPeriod" size="small">最近周期 {{ summary.lastPeriod }}</a-tag>
              <template v-if="summary.total">
                <a-tag size="small" color="green">正常 {{ summary.complete }}</a-tag>
                <a-tag v-if="summary.degraded" size="small" color="orange">降级 {{ summary.degraded }}</a-tag>
                <a-tag v-if="summary.skipped" size="small" color="gray">跳过 {{ summary.skipped }}</a-tag>
              </template>
            </template>
            <template #query-extra>
              <FactorConditionBar :conditions="conditions" :scope="scope" @update:conditions="setConditions" />
            </template>
            <template #toolbar-extra>
              <a-button size="small" :disabled="!scopeReady" @click="settingsVisible = true">
                <template #icon><icon-settings /></template>列设置
              </a-button>
            </template>
            <template #row-extra="{ rowKey }">
              <a-link v-if="scope.outputs.length" :disabled="!rowKey" @click="openValues(rowKey)"
                >查看全部 {{ scope.outputs.length }} 个</a-link
              >
              <span v-else class="muted">-</span>
            </template>
          </ViewBrowse>
          <a-spin v-else-if="resultLoading" class="results-loading" />
          <a-empty v-else description="计算任务还没有结果数据集，请稍后刷新">
            <template #extra>
              <a-button @click="goTasks">前往计算任务</a-button>
            </template>
          </a-empty>

          <FactorValuesModal
            v-if="resultView"
            v-model:visible="valuesVisible"
            :dataset-id="resultView.dataset_id"
            :row-key="valuesRowKey"
            :scope="scope"
          />
          <ColumnSettingsDrawer
            v-model:visible="settingsVisible"
            :scope="scope"
            :settings="effectiveSettings"
            @update:settings="updateSettings"
          />
        </template>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useDisplayZone } from "@/hooks/useDisplayZone";
import { formatInstant } from "@/utils/time-zone";
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { listFactorCategories } from "@/api/factor";
import type { FactorCategory, FactorTaskInfo } from "@/api/factor/types";
import { listAllDatasetColumns } from "@/api/storage/metadata";
import type { DatasetColumn, RowKey } from "@/api/storage/types";
import { getStrategyDisplaySettings } from "@/api/strategy";
import type { DisplayHidden } from "@/api/strategy-types";
import { createLatestRequestGuard, type LatestRequest } from "@/utils/latest-request";
import ViewBrowse from "@/views/data/view-browse/index.vue";
import TaskTabs from "@/views/factor/shared/task-tabs.vue";
import { useFactorScope } from "@/views/factor/shared/use-factor-scope";
import { resolveActiveTaskId, resultTasks, resultSummary } from "./results-model";
import { buildFactorScope } from "./factor-scope";
import { decodeConditions, encodeConditions, toFilterState, type FactorCondition } from "./factor-condition";
import {
  defaultColumnSettings,
  loadColumnSettings,
  normalizeColumnSettings,
  saveColumnSettings,
  tableColumns,
  type ColumnSettings
} from "./column-settings";
import FactorConditionBar from "./factor-condition-bar.vue";
import FactorValuesModal from "./factor-values-modal.vue";
import ColumnSettingsDrawer from "./column-settings-drawer.vue";

defineOptions({ name: "FactorResults" });

// 页面时间按当前空间登记的时区显示
const displayZone = useDisplayZone();
const route = useRoute();
const router = useRouter();
const { store, spaceId, selectTask, navigate, reload } = useFactorScope({ requireTask: true });

const available = computed<FactorTaskInfo[]>(() => resultTasks(store.tasks));
// 任务页签只列能浏览结果的任务（补算页列全部任务）
const isResultTask = (info: FactorTaskInfo) => resultTasks([info]).length > 0;
const activeTaskId = ref("");
const activeInfo = computed(() =>
  available.value.find((item: FactorTaskInfo) => item.factor_task.task_id === activeTaskId.value)
);
const summary = computed(() => resultSummary(activeInfo.value ?? {}, displayZone.value));

/** 计算任务的结果数据集：每个数据集恰好有一个查询索引，直接按它浏览。 */
const resultView = ref<{ space_id: string; dataset_id: string } | null>(null);
const resultLoading = ref(false);
const refreshKey = ref(0);
const readAt = ref("");
const viewGate = createLatestRequestGuard();

// 有效范围的原料：结果数据集启用的列、分类与策略外显设置；成员清单直接来自 store（随轮询更新）。
const resultColumns = ref<DatasetColumn[]>([]);
const categories = ref<FactorCategory[]>([]);
const displayHidden = ref<DisplayHidden>();
/** 原料全部就绪后才放行首查，否则首查会带着默认投影、漏掉 URL 里的因子条件。 */
const scopeReady = ref(false);
const scopeError = ref("");
const scope = computed(() =>
  buildFactorScope({
    columns: resultColumns.value,
    members: activeInfo.value?.members ?? [],
    categories: categories.value,
    hidden: displayHidden.value
  })
);

const settings = ref<ColumnSettings>(defaultColumnSettings(scope.value));
const settingsVisible = ref(false);
/** 成员变化后（例如被移除）只在展示时收敛，不改写已保存的设置。 */
const effectiveSettings = computed(() => normalizeColumnSettings(settings.value, scope.value));
const table = computed(() => tableColumns(effectiveSettings.value, scope.value));

const conditions = ref<FactorCondition[]>([]);
const extraFilters = computed(() =>
  conditions.value.filter(item => scope.value.outputByColumn.has(item.column)).map(toFilterState)
);

const valuesVisible = ref(false);
const valuesRowKey = ref<RowKey>();

function stamp() {
  readAt.value = formatInstant(Date.now(), displayZone.value, "clock");
}

async function loadResultView() {
  const current = activeInfo.value?.factor_task;
  const token = viewGate.begin();
  resultView.value = null;
  scopeReady.value = false;
  scopeError.value = "";
  if (!current?.result_dataset_id) {
    resultLoading.value = false;
    return;
  }
  resultView.value = { space_id: current.space_id, dataset_id: current.result_dataset_id };
  resultLoading.value = true;
  try {
    await loadScope(current, token);
  } finally {
    if (token.isLatest()) resultLoading.value = false;
  }
}

async function loadActiveColumns(spaceId: string, datasetId: string) {
  const columns = await listAllDatasetColumns(spaceId, datasetId);
  return columns.filter(column => column.status === "active");
}

async function loadScope(current: FactorTaskInfo["factor_task"], token: LatestRequest) {
  try {
    const [columns, categoryList, hidden] = await Promise.all([
      loadActiveColumns(current.space_id, current.result_dataset_id),
      // 分类只用于展示名称和排序，读取失败时按 ID 展示。
      listFactorCategories().catch(() => [] as FactorCategory[]),
      // 外显设置只影响展示：读取失败时全部展示。
      current.bound_instance_id
        ? getStrategyDisplaySettings(current.bound_instance_id).catch(() => undefined)
        : Promise.resolve(undefined)
    ]);
    if (!token.isLatest()) return;
    resultColumns.value = columns;
    categories.value = categoryList;
    displayHidden.value = hidden;
    settings.value = loadColumnSettings(current.task_id, scope.value);
    applyRouteConditions(true);
    scopeReady.value = true;
  } catch (error) {
    if (!token.isLatest()) return;
    scopeError.value = `结果列读取失败：${error instanceof Error ? error.message : String(error)}`;
  }
}

/** 从 ?fc= 恢复因子条件；不在有效范围或不合法的条件丢弃并提示。 */
function applyRouteConditions(notify: boolean) {
  const { conditions: restored, dropped } = decodeConditions(route.query.fc, new Set(scope.value.outputByColumn.keys()));
  conditions.value = restored;
  if (notify && dropped) Message.warning(`已忽略 ${dropped} 个失效的因子条件`);
  if (dropped) void navigate({ fc: encodeConditions(restored) || undefined });
}

function setConditions(next: FactorCondition[]) {
  conditions.value = next;
  const encoded = encodeConditions(next);
  if (encoded !== (route.query.fc ?? "")) void navigate({ fc: encoded || undefined });
}

function updateSettings(next: ColumnSettings) {
  settings.value = next;
  const taskId = activeInfo.value?.factor_task.task_id;
  if (taskId) saveColumnSettings(taskId, next);
}

function openValues(rowKey?: RowKey) {
  if (!rowKey) return;
  valuesRowKey.value = rowKey;
  valuesVisible.value = true;
}

function onTabChange(key: string | number) {
  selectTask(String(key));
}

/**
 * “刷新结果”：重读任务状态和结果列，表格只重读行（不重建浏览，筛选保留）；结果数据集还没有时才重新找。
 * 结果列也要重读：成员启用时后端才补结果列，可能比成员状态晚一步。
 */
async function refreshResults() {
  await store.reload();
  if (!resultView.value) {
    await loadResultView();
    return;
  }
  await reloadResultColumns();
  refreshKey.value += 1;
}

/** 成员变了（添加、移除、启停）：结果列跟着变，只重读结果列，不重建浏览。 */
let columnsSeq = 0;
async function reloadResultColumns() {
  const view = resultView.value;
  if (!view || !scopeReady.value) return;
  const seq = ++columnsSeq;
  try {
    const columns = await loadActiveColumns(view.space_id, view.dataset_id);
    if (seq === columnsSeq && resultView.value?.dataset_id === view.dataset_id) resultColumns.value = columns;
  } catch {
    // 读不到时沿用上次的列，下次成员变化或点“刷新结果”时再读。
  }
}

function goTasks() {
  void router.push("/factor/tasks");
}

// 选中项跟随 store.currentTaskId（?task= 已由 use-factor-scope 同步）；若它不在可浏览任务里，回落到第一个。
watch(
  () => [store.currentTaskId, available.value.map((item: FactorTaskInfo) => item.factor_task.task_id).join(",")],
  () => {
    const next = resolveActiveTaskId(available.value, store.currentTaskId);
    if (next !== activeTaskId.value) activeTaskId.value = next;
  },
  { immediate: true }
);

watch(
  () => activeInfo.value?.factor_task.result_dataset_id,
  () => void loadResultView(),
  { immediate: true }
);

// 浏览器前进后退改了 ?fc= 时跟随（页面自己写入的不会触发变化）。
watch(
  () => route.query.fc,
  value => {
    if (scopeReady.value && (value ?? "") !== encodeConditions(conditions.value)) applyRouteConditions(false);
  }
);

watch(() => store.tasks, stamp, { immediate: true });
watch(
  // 启用时后端才补结果列，所以状态也算在内
  () => (activeInfo.value?.members ?? []).map(member => `${member.member_key || member.factor_id}:${member.status}`).join(","),
  (keys, previous) => {
    if (previous !== undefined && keys !== previous) void reloadResultColumns();
  }
);
</script>

<style scoped lang="scss">
@use "../shared/factor-page.scss";

.factor-results-page {
  width: 100%;
  max-width: 100%;
  min-width: 0;
  min-height: 100%;
  overflow-x: hidden;
}

.results-alert {
  margin-bottom: var(--moox-space-3);
}

.result-tabs {
  flex: 1;
}

.result-actions {
  flex: none;
}

.result-read-at {
  color: var(--color-text-3);
  font-size: 13px;
}

.results-loading {
  display: flex;
  justify-content: center;
  padding: var(--moox-space-8);
}

.muted {
  color: var(--color-text-3);
}
</style>

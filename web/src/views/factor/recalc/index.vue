<template>
  <div class="moox-page recalc-page">
    <div class="moox-inner">
      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <a-alert v-if="store.loadError" type="error" show-icon class="recalc-alert">
          {{ store.loadError }}
          <template #action><a-button size="mini" @click="reload">重试</a-button></template>
        </a-alert>

        <a-empty v-if="!store.sets.length && !store.loading" description="当前空间没有计算任务">
          <template #extra>
            <a-button @click="goTasks">前往计算任务</a-button>
          </template>
        </a-empty>

        <template v-else>
          <div class="result-toolbar">
            <span class="result-count">
              共 {{ pagination.total }} 条补算任务
              <InfoTip text="启用因子时的历史回填与手动补算都在这里；有进行中的任务时每 3 秒自动刷新。" />
            </span>
            <a-space wrap>
              <SetSelect :model-value="store.currentSetId" @change="onSetChange" />
              <a-button :loading="loading" @click="refresh">
                <template #icon><icon-refresh /></template>
                刷新
              </a-button>
              <a-button type="primary" status="success" :disabled="!set" @click="createVisible = true">
                <template #icon><icon-plus /></template>
                新建补算
              </a-button>
            </a-space>
          </div>

          <div class="recalc-filter">
            <a-radio-group v-model="segment" type="button" aria-label="补算状态筛选" @change="onSegmentChange">
              <a-radio v-for="item in STATUS_SEGMENTS" :key="item.key" :value="item.key">{{ item.label }}</a-radio>
            </a-radio-group>
          </div>

          <a-table
            row-key="job_id"
            size="small"
            :bordered="{ cell: true }"
            :loading="loading"
            :data="rows"
            :pagination="pagination"
            :row-class="rowClass"
            :scroll="{ x: 'max-content' }"
            @page-change="onPageChange"
            @page-size-change="onPageSizeChange"
          >
            <template #empty><a-empty description="该计算任务还没有补算任务" /></template>
            <template #columns>
              <a-table-column title="任务" :width="260">
                <template #cell="{ record }">
                  <div class="job-id" :title="record.job.job_id">{{ record.job.job_id }}</div>
                  <a-tag size="small" :color="record.source.kind === 'enable' ? 'arcoblue' : 'gray'">{{
                    record.source.label
                  }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="因子" :width="180" :ellipsis="true" :tooltip="true">
                <template #cell="{ record }">{{ record.job.factor_ids.join(", ") || "全部已启用因子" }}</template>
              </a-table-column>
              <a-table-column title="时间范围" :width="260">
                <template #cell="{ record }">
                  {{ formatTime(record.job.start_time) }}<br />
                  → {{ formatTime(record.job.end_time) }}
                </template>
              </a-table-column>
              <a-table-column title="状态" :width="200">
                <template #cell="{ record }">
                  <a-tag size="small" :color="record.display.statusColor">{{ record.display.statusLabel }}</a-tag>
                  <a-tooltip v-if="record.display.degraded" :content="record.display.degradedText">
                    <a-tag size="small" color="orange">部分降级</a-tag>
                  </a-tooltip>
                  <div v-if="record.display.errorText" class="job-error" :title="record.display.errorText">
                    {{ record.display.errorText }}
                  </div>
                </template>
              </a-table-column>
              <a-table-column title="进度" :width="190">
                <template #cell="{ record }">
                  <a-progress :percent="record.progress / 100" :status="record.job.status === 'failed' ? 'danger' : undefined" />
                </template>
              </a-table-column>
              <a-table-column title="创建时间" :width="180">
                <template #cell="{ record }">{{ formatTime(record.job.created_at) }}</template>
              </a-table-column>
              <a-table-column title="操作" :width="90" align="center" fixed="right">
                <template #cell="{ record }">
                  <a-popconfirm content="取消该补算任务？" @ok="cancel(record.job)">
                    <a-button size="mini" type="text" status="danger" :disabled="!isActiveJob(record.job)">取消</a-button>
                  </a-popconfirm>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </template>
      </template>
    </div>

    <RecalcCreateDrawer v-model:visible="createVisible" :info="store.current" @submitted="onSubmitted" />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { cancelRecalcJob, listRecalcJobs } from "@/api/factor";
import type { FactorSet, RecalcJob } from "@/api/factor/types";
import { usePolling } from "@/hooks/usePolling";
import { RequestGate } from "@/utils/request-gate";
import { applyPageResult, defaultPagination, formatTime } from "@/views/data/shared/metadata-utils";
import InfoTip from "@/views/factor/shared/info-tip.vue";
import SetSelect from "@/views/factor/shared/set-select.vue";
import { isActiveJob } from "@/views/factor/shared/status";
import { useFactorScope } from "@/views/factor/shared/use-factor-scope";
import RecalcCreateDrawer from "./recalc-create-drawer.vue";
import { STATUS_SEGMENTS, jobDisplay, jobSource, progressPercent, segmentStatuses, type StatusSegment } from "./recalc-model";

defineOptions({ name: "FactorRecalc" });

const route = useRoute();
const router = useRouter();
const { store, spaceId, selectSet, navigate, reload } = useFactorScope({ requireSet: true, poll: false });

const set = computed<FactorSet | undefined>(() => store.current?.factor_set);
const highlightJob = computed(() => {
  const value = route.query.job;
  return typeof value === "string" ? value : "";
});

const jobs = ref<RecalcJob[]>([]);
const loading = ref(false);
const createVisible = ref(false);
const segment = ref<StatusSegment>("all");
const pagination = reactive(defaultPagination());
const gate = new RequestGate();

const rows = computed(() =>
  jobs.value.map(job => ({ job, source: jobSource(job), display: jobDisplay(job), progress: progressPercent(job) }))
);
const rowClass = (record: { job_id?: string }) =>
  record.job_id && record.job_id === highlightJob.value ? "recalc-row--highlight" : "";

async function load(options: { silent?: boolean } = {}) {
  const current = set.value;
  const token = gate.next();
  if (!current) {
    jobs.value = [];
    return;
  }
  if (!options.silent) loading.value = true;
  try {
    const statuses = segmentStatuses(segment.value);
    const rsp = await listRecalcJobs({
      set_id: current.set_id,
      ...(statuses ? { statuses } : {}),
      page: { page: pagination.current, size: pagination.pageSize }
    });
    if (!gate.isCurrent(token)) return;
    jobs.value = rsp.jobs || [];
    applyPageResult(pagination, rsp.page_result);
    void scrollToHighlight();
  } catch (error) {
    if (gate.isCurrent(token) && !options.silent) Message.error(error instanceof Error ? error.message : "补算任务加载失败");
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

async function scrollToHighlight() {
  if (!highlightJob.value) return;
  await nextTick();
  document.querySelector(".recalc-row--highlight")?.scrollIntoView({ block: "center", behavior: "smooth" });
}

function refresh() {
  void store.reload();
  return load();
}

function onSetChange(setId: string) {
  selectSet(setId);
}

function onSegmentChange() {
  pagination.current = 1;
  void load();
}

function onPageChange(page: number) {
  pagination.current = page;
  void load();
}

function onPageSizeChange(size: number) {
  pagination.current = 1;
  pagination.pageSize = size;
  void load();
}

async function cancel(job: RecalcJob) {
  try {
    await cancelRecalcJob(job.job_id);
    Message.success("补算任务已取消");
    await load({ silent: true });
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "取消补算任务失败");
  }
}

async function onSubmitted(job: RecalcJob) {
  segment.value = "all";
  pagination.current = 1;
  await navigate({ job: job.job_id });
  await load();
}

function goTasks() {
  void router.push("/factor/tasks");
}

usePolling(
  () => load({ silent: true }),
  3000,
  () => jobs.value.some(isActiveJob)
);

watch(
  () => set.value?.set_id,
  () => {
    pagination.current = 1;
    void load();
  },
  { immediate: true }
);
</script>

<style scoped lang="scss">
@use "../shared/factor-page.scss";

.recalc-alert {
  margin-bottom: var(--moox-space-3);
}

.recalc-filter {
  margin-bottom: var(--moox-space-2);
}

.job-id {
  overflow: hidden;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.job-error {
  max-width: 220px;
  overflow: hidden;
  color: rgb(var(--danger-6));
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

:deep(.recalc-row--highlight td) {
  background: var(--color-primary-light-1) !important;
}
</style>

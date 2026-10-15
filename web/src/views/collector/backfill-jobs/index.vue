<template>
  <div class="moox-page backfill-page">
    <div class="moox-inner">
      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <div class="page-toolbar">
          <span class="result-count">
            共 {{ total }} 条补采作业
            <a-tooltip
              content="自动补采发现实时缺口时建作业；手动补采、历史缺口的补采确认也在这里。有进行中的作业时每 5 秒自动刷新。"
            >
              <icon-question-circle class="count-tip" />
            </a-tooltip>
          </span>
          <a-space wrap>
            <a-select v-model="datasetId" placeholder="全部数据集" allow-clear style="width: 220px" @change="onFilterChange">
              <a-option v-for="item in datasetOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
            </a-select>
            <a-button :loading="loading" @click="reload">
              <template #icon><icon-refresh /></template>
              刷新
            </a-button>
            <a-button @click="openRange('check')">
              <template #icon><icon-search /></template>
              检查缺口
            </a-button>
            <a-button type="primary" status="success" @click="openRange('backfill')">
              <template #icon><icon-plus /></template>
              新建补采
            </a-button>
          </a-space>
        </div>

        <div class="segment-filter">
          <a-radio-group v-model="segment" type="button" aria-label="补采作业筛选" @change="onFilterChange">
            <a-radio v-for="item in JOB_SEGMENTS" :key="item.key" :value="item.key">{{ item.label }}</a-radio>
          </a-radio-group>
        </div>

        <a-table
          row-key="job_id"
          size="small"
          :data="jobs"
          :loading="loading"
          :bordered="{ cell: true }"
          :pagination="{ current: page, pageSize, total, showTotal: true, size: 'small' }"
          :row-class="rowClass"
          table-layout-fixed
          @page-change="onPageChange"
        >
          <template #empty>
            <a-empty :description="segment === 'attention' ? '没有需要关注的补采作业' : '暂无补采作业'" />
          </template>
          <template #columns>
            <a-table-column title="作业" :width="190">
              <template #cell="{ record }">
                <a-link class="job-id" :title="record.job_id" @click="openDetail(record)">{{ record.job_id }}</a-link>
                <a-space :size="4">
                  <a-tag size="small" :color="record.source === 'auto' ? 'arcoblue' : 'gray'">{{
                    jobSourceText(record.source)
                  }}</a-tag>
                  <a-tag size="small">{{ jobKindText(record.kind) }}</a-tag>
                </a-space>
              </template>
            </a-table-column>
            <a-table-column title="数据集">
              <template #cell="{ record }">
                <div class="ellipsis" :title="record.dataset_id">{{ jobDatasetText(record) }}</div>
                <div v-if="record.subject_id" class="muted">标的 {{ record.subject_id }}</div>
              </template>
            </a-table-column>
            <a-table-column title="时间范围（UTC）" :width="150">
              <template #cell="{ record }"> {{ utcText(record.from) }}<br />→ {{ utcText(record.to) }} </template>
            </a-table-column>
            <a-table-column title="状态" :width="170">
              <template #cell="{ record }">
                <a-tag size="small" :color="jobStatusInfo(record.status).color">{{ jobStatusInfo(record.status).text }}</a-tag>
                <div v-if="record.last_error" class="job-error" :title="record.last_error">{{ record.last_error }}</div>
              </template>
            </a-table-column>
            <a-table-column title="进度" :width="150">
              <template #cell="{ record }">
                <a-tooltip
                  :content="`成功 ${record.succeeded || 0} / 失败 ${record.failed || 0} / 执行中 ${record.running || 0} / 未开始 ${record.pending || 0}`"
                >
                  <div class="segments">
                    <span class="seg-ok" :style="{ width: `${jobSegments(record).succeeded}%` }" />
                    <span class="seg-fail" :style="{ width: `${jobSegments(record).failed}%` }" />
                    <span class="seg-run" :style="{ width: `${jobSegments(record).running}%` }" />
                  </div>
                </a-tooltip>
                <div class="muted">{{ jobSegments(record).done }} / {{ jobSegments(record).total }} 块</div>
              </template>
            </a-table-column>
            <a-table-column title="创建时间" :width="170">
              <template #cell="{ record }">{{ localTimeText(record.created_at) }}</template>
            </a-table-column>
            <a-table-column title="操作" :width="130" align="center" fixed="right">
              <template #cell="{ record }">
                <a-button size="mini" type="text" @click="openDetail(record)">详情</a-button>
                <a-popconfirm v-if="jobRetryable(record)" content="失败的分块重新执行，确认重试？" @ok="retry(record)">
                  <a-button size="mini" type="text">重试</a-button>
                </a-popconfirm>
                <a-popconfirm
                  v-if="jobActive(record)"
                  content="尚未开始的分块不再执行，运行中的分块会等其结束。确认取消？"
                  @ok="cancel(record)"
                >
                  <a-button size="mini" type="text" status="danger">取消</a-button>
                </a-popconfirm>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </template>
    </div>

    <KlineRangeDialog
      v-model:visible="rangeVisible"
      :mode="rangeMode"
      :space-id="spaceId"
      :dataset-options="datasetOptions"
      :dataset-id="datasetId"
      @submitted="onSubmitted"
    />

    <a-drawer :visible="Boolean(detail)" :width="760" :footer="false" unmount-on-close title="补采作业详情" @cancel="closeDetail">
      <template v-if="detail?.job">
        <div class="detail-head">
          <a-tag :color="jobStatusInfo(detail.job.status).color">{{ jobStatusInfo(detail.job.status).text }}</a-tag>
          <span class="mono-text">{{ detail.job.job_id }}</span>
          <a-space class="detail-actions">
            <a-popconfirm v-if="jobRetryable(detail.job)" content="失败的分块重新执行，确认重试？" @ok="retry(detail.job)">
              <a-button size="small" type="primary">重试失败分块</a-button>
            </a-popconfirm>
            <a-popconfirm
              v-if="jobActive(detail.job)"
              content="尚未开始的分块不再执行，运行中的分块会等其结束。确认取消？"
              @ok="cancel(detail.job)"
            >
              <a-button size="small" status="danger">取消作业</a-button>
            </a-popconfirm>
          </a-space>
        </div>
        <a-descriptions :column="2" bordered size="small">
          <a-descriptions-item label="数据集">{{ jobDatasetText(detail.job) }}</a-descriptions-item>
          <a-descriptions-item label="来源"
            >{{ jobSourceText(detail.job.source) }} · {{ jobKindText(detail.job.kind) }}</a-descriptions-item
          >
          <a-descriptions-item label="标的">{{ detail.job.subject_id || "全部" }}</a-descriptions-item>
          <a-descriptions-item label="创建时间">{{ localTimeText(detail.job.created_at) }}</a-descriptions-item>
          <a-descriptions-item label="范围（UTC）" :span="2"
            >{{ utcText(detail.job.from) }} ~ {{ utcText(detail.job.to) }}</a-descriptions-item
          >
          <a-descriptions-item label="分块" :span="2">
            成功 {{ detail.job.succeeded || 0 }} / 失败 {{ detail.job.failed || 0 }} / 执行中 {{ detail.job.running || 0 }} /
            未开始 {{ detail.job.pending || 0 }} / 取消 {{ detail.job.canceled || 0 }}
          </a-descriptions-item>
          <a-descriptions-item v-if="detail.job.last_error" label="错误" :span="2">{{
            detail.job.last_error
          }}</a-descriptions-item>
        </a-descriptions>
        <h4 class="section-title">失败分块（至多 50 个）</h4>
        <a-table row-key="key" size="small" :data="failedRows" :pagination="false" :bordered="{ cell: true }" table-layout-fixed>
          <template #empty><div class="empty-text">没有失败的分块</div></template>
          <template #columns>
            <a-table-column v-if="!detail.job.dataset_id" title="数据集" :width="150">
              <template #cell="{ record }"
                ><span class="ellipsis" :title="record.dataset_id">{{ record.dataset_name || record.dataset_id }}</span></template
              >
            </a-table-column>
            <a-table-column title="标的" data-index="subject_id" :width="120" />
            <a-table-column title="范围（UTC）" :width="230">
              <template #cell="{ record }">{{ utcText(record.from) }} ~ {{ utcText(record.to) }}</template>
            </a-table-column>
            <a-table-column title="尝试" data-index="attempts" :width="70" align="center" />
            <a-table-column title="错误">
              <template #cell="{ record }"
                ><span class="chunk-error">{{ record.last_error || "-" }}</span></template
              >
            </a-table-column>
          </template>
        </a-table>
      </template>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { storeToRefs } from "pinia";
import {
  cancelKlineBackfill,
  getCollectorStatus,
  getKlineBackfill,
  listKlineBackfills,
  retryKlineBackfill,
  type BackfillJobFilter,
  type KlineBackfillChunk,
  type KlineBackfillJob
} from "@/api/collector/runtime-status";
import { usePolling } from "@/hooks/usePolling";
import { useSpaceStore } from "@/store/modules/space";
import { createLatestRequestGuard } from "@/utils/latest-request";
import KlineRangeDialog from "./kline-range-dialog.vue";
import {
  JOB_SEGMENTS,
  jobActive,
  jobDatasetText,
  jobKindText,
  jobRetryable,
  jobSegments,
  jobSourceText,
  jobStatusInfo,
  localTimeText,
  normalizeSegment,
  utcText,
  type JobSegment
} from "./backfill-model";

defineOptions({ name: "CollectorBackfillJobs" });

const route = useRoute();
const router = useRouter();
const { selectedSpaceId } = storeToRefs(useSpaceStore());
const spaceId = computed(() => selectedSpaceId.value || "");
const guard = createLatestRequestGuard();

const queryText = (value: unknown) => (typeof value === "string" ? value : "");
const segment = ref<JobSegment>(normalizeSegment(route.query.segment));
const datasetId = ref(queryText(route.query.dataset));
const highlightJob = computed(() => queryText(route.query.job));

const jobs = ref<KlineBackfillJob[]>([]);
const loading = ref(false);
const page = ref(1);
const pageSize = 20;
const total = ref(0);
const datasetOptions = ref<{ value: string; label: string }[]>([]);
const detail = ref<{ job?: KlineBackfillJob; failed_chunks?: KlineBackfillChunk[] } | null>(null);
const rangeVisible = ref(false);
const rangeMode = ref<"check" | "backfill">("check");

const failedRows = computed(() =>
  (detail.value?.failed_chunks || []).map((item, index) => ({
    ...item,
    key: `${item.dataset_id}|${item.subject_id}|${item.from}|${index}`
  }))
);
const rowClass = (record: KlineBackfillJob) =>
  record.job_id && record.job_id === highlightJob.value ? "backfill-row--highlight" : "";

function filterOf(): BackfillJobFilter {
  const filter: BackfillJobFilter = datasetId.value ? { dataset_id: datasetId.value } : {};
  if (segment.value === "attention") filter.attention = true;
  if (segment.value === "manual" || segment.value === "auto") filter.source = segment.value;
  return filter;
}

async function load(options: { silent?: boolean } = {}) {
  const space = spaceId.value;
  if (!space) return;
  const request = guard.begin();
  if (!options.silent) loading.value = true;
  try {
    const data = await listKlineBackfills(space, filterOf(), page.value, pageSize);
    if (!request.isLatest() || space !== spaceId.value) return;
    jobs.value = data.items || [];
    total.value = Number(data.page?.total || 0);
  } catch (error) {
    if (request.isLatest() && !options.silent) Message.error(error instanceof Error ? error.message : "补采作业加载失败");
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

/** 数据集下拉的展示名来自运行状态（只列正在采集的数据集）。 */
async function loadDatasets() {
  const space = spaceId.value;
  if (!space) return;
  try {
    const status = await getCollectorStatus(space);
    if (space !== spaceId.value) return;
    const options = new Map<string, string>();
    for (const item of status.datasets || []) {
      if (item.dataset_id && !options.has(item.dataset_id)) options.set(item.dataset_id, item.dataset_name || item.dataset_id);
    }
    datasetOptions.value = [...options]
      .map(([value, label]) => ({ value, label }))
      .sort((a, b) => a.label.localeCompare(b.label));
  } catch (error) {
    console.error("获取数据集列表失败:", error);
  }
}

function reload() {
  void loadDatasets();
  return load();
}

/** 筛选写回 URL，刷新或分享链接后保持同一视图。 */
function syncQuery(extra: Record<string, string | undefined> = {}) {
  void router.replace({
    path: "/collector/tasks",
    query: {
      tab: "backfills",
      segment: segment.value === "attention" ? undefined : segment.value,
      dataset: datasetId.value || undefined,
      ...extra
    }
  });
}

function onFilterChange() {
  page.value = 1;
  syncQuery();
  void load();
}

function onPageChange(value: number) {
  page.value = value;
  void load();
}

async function openDetail(record: KlineBackfillJob) {
  try {
    detail.value = await getKlineBackfill(spaceId.value, record.job_id || "");
    if (highlightJob.value !== record.job_id) syncQuery({ job: record.job_id });
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "获取补采作业详情失败");
  }
}

function closeDetail() {
  detail.value = null;
  syncQuery();
}

async function act(job: KlineBackfillJob, action: () => Promise<unknown>, success: string) {
  try {
    await action();
    Message.success(success);
    if (detail.value?.job?.job_id === job.job_id) detail.value = await getKlineBackfill(spaceId.value, job.job_id || "");
    await load({ silent: true });
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "操作失败");
  }
}

const retry = (job: KlineBackfillJob) =>
  act(job, () => retryKlineBackfill(spaceId.value, job.job_id || ""), "已重新排队失败分块");
const cancel = (job: KlineBackfillJob) => act(job, () => cancelKlineBackfill(spaceId.value, job.job_id || ""), "已取消");

function openRange(mode: "check" | "backfill") {
  rangeMode.value = mode;
  rangeVisible.value = true;
}

function onSubmitted(jobId: string) {
  segment.value = "attention";
  page.value = 1;
  syncQuery({ job: jobId });
  void load();
}

usePolling(
  () => load({ silent: true }),
  5000,
  () => jobs.value.some(jobActive)
);

// 从运行状态页带着作业跳转过来时，直接打开它的详情。
watch(
  highlightJob,
  jobId => {
    if (jobId && detail.value?.job?.job_id !== jobId && spaceId.value) void openDetail({ job_id: jobId });
  },
  { immediate: true }
);

watch(
  spaceId,
  () => {
    guard.invalidate();
    jobs.value = [];
    total.value = 0;
    page.value = 1;
    detail.value = null;
    void reload();
  },
  { immediate: true }
);
</script>

<style scoped>
.page-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-3);
}
.result-count {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--color-text-2);
  font-size: 13px;
}
.count-tip {
  color: var(--color-text-3);
  cursor: help;
}
.segment-filter {
  margin-bottom: var(--moox-space-2);
}
.job-id {
  display: block;
  max-width: 100%;
  overflow: hidden;
  padding: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.job-error {
  max-width: 100%;
  overflow: hidden;
  color: rgb(var(--danger-6));
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.segments {
  display: flex;
  height: 6px;
  overflow: hidden;
  border-radius: 3px;
  background: var(--color-fill-3);
}
.seg-ok {
  background: rgb(var(--success-6));
}
.seg-fail {
  background: rgb(var(--danger-6));
}
.seg-run {
  background: rgb(var(--arcoblue-6));
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.ellipsis {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mono-text {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
}
.empty-text {
  padding: var(--moox-space-4) 0;
  color: var(--color-text-3);
  text-align: center;
}
.detail-head {
  display: flex;
  align-items: center;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-3);
}
.detail-actions {
  margin-left: auto;
}
.section-title {
  margin: var(--moox-space-4) 0 var(--moox-space-2);
  font-size: 13px;
}
.chunk-error {
  color: rgb(var(--danger-6));
  word-break: break-all;
}
:deep(.backfill-row--highlight td) {
  background: var(--color-primary-light-1) !important;
}
</style>

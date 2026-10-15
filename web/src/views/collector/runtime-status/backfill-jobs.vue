<template>
  <div class="backfill-jobs">
    <div class="pane-toolbar">
      <a-radio-group v-model="segment" type="button" size="small" aria-label="补采作业筛选" @change="onSegmentChange">
        <a-radio value="attention">需关注</a-radio>
        <a-radio value="manual">手动</a-radio>
        <a-radio value="auto">自动</a-radio>
        <a-radio value="all">全部</a-radio>
      </a-radio-group>
      <a-space>
        <a-button size="small" @click="emit('check')">
          <template #icon><icon-search /></template>
          检查缺口
        </a-button>
        <a-button size="small" type="primary" status="success" @click="emit('backfill')">
          <template #icon><icon-plus /></template>
          新建补采
        </a-button>
      </a-space>
    </div>
    <a-table
      row-key="job_id"
      size="small"
      :data="jobs"
      :loading="loading"
      :bordered="{ cell: true }"
      :pagination="{ current: page, pageSize, total, showTotal: true, size: 'small' }"
      class="clickable-table"
      @page-change="onPageChange"
      @row-click="openDetail"
    >
      <template #empty>
        <div class="empty-text">{{ segment === "attention" ? "没有需要关注的补采作业" : "暂无补采作业" }}</div>
      </template>
      <template #columns>
        <a-table-column title="数据集 / 范围">
          <template #cell="{ record }">
            <div class="mono-text ellipsis">{{ jobScopeText(record) }}</div>
            <div class="muted">{{ jobRangeText(record) }}</div>
          </template>
        </a-table-column>
        <a-table-column title="来源" :width="110">
          <template #cell="{ record }">
            <div>{{ jobSourceText(record.source) }}</div>
            <div class="muted">{{ jobKindText(record.kind) }}</div>
          </template>
        </a-table-column>
        <a-table-column title="进度" :width="220">
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
        <a-table-column title="错误">
          <template #cell="{ record }">
            <span class="ellipsis" :class="{ 'danger-text': record.last_error }">{{ record.last_error || "-" }}</span>
          </template>
        </a-table-column>
        <a-table-column title="创建时间" :width="180">
          <template #cell="{ record }">{{ localTimeText(record.created_at) }}</template>
        </a-table-column>
        <a-table-column title="状态" :width="90" align="center">
          <template #cell="{ record }">
            <a-tag size="small" :color="jobStatusInfo(record.status).color">{{ jobStatusInfo(record.status).text }}</a-tag>
          </template>
        </a-table-column>
      </template>
    </a-table>
    <div class="retention-hint">自动作业保留 7 天、手动作业保留 30 天；点击行查看详情。</div>

    <a-drawer
      :visible="Boolean(detail)"
      :width="720"
      :footer="false"
      unmount-on-close
      title="补采作业详情"
      @cancel="detail = null"
    >
      <template v-if="detail?.job">
        <div class="detail-head">
          <a-tag :color="jobStatusInfo(detail.job.status).color">{{ jobStatusInfo(detail.job.status).text }}</a-tag>
          <span class="mono-text">{{ detail.job.job_id }}</span>
          <a-space class="detail-actions">
            <a-popconfirm v-if="backfillRetryable(detail.job)" content="失败的分块重新执行，确认重试？" @ok="onRetry">
              <a-button size="small" type="primary" :loading="acting">重试失败分块</a-button>
            </a-popconfirm>
            <a-popconfirm
              v-if="backfillCancelable(detail.job)"
              content="尚未开始的分块不再执行，运行中的分块会等其结束。确认取消？"
              @ok="onCancel"
            >
              <a-button size="small" status="danger" :loading="acting">取消作业</a-button>
            </a-popconfirm>
          </a-space>
        </div>
        <a-descriptions :column="2" bordered size="small">
          <a-descriptions-item label="数据集">{{ jobScopeText(detail.job) }}</a-descriptions-item>
          <a-descriptions-item label="来源"
            >{{ jobSourceText(detail.job.source) }} · {{ jobKindText(detail.job.kind) }}</a-descriptions-item
          >
          <a-descriptions-item label="范围" :span="2">{{ jobRangeText(detail.job) }}</a-descriptions-item>
          <a-descriptions-item label="分块" :span="2">
            成功 {{ detail.job.succeeded || 0 }} / 失败 {{ detail.job.failed || 0 }} / 执行中 {{ detail.job.running || 0 }} /
            未开始 {{ detail.job.pending || 0 }} / 取消 {{ detail.job.canceled || 0 }}
          </a-descriptions-item>
          <a-descriptions-item label="创建时间">{{ localTimeText(detail.job.created_at) }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.job.last_error" label="错误" :span="2">{{
            detail.job.last_error
          }}</a-descriptions-item>
        </a-descriptions>
        <h4 class="section-title">失败分块（至多 50 个）</h4>
        <a-table row-key="key" size="small" :data="failedRows" :pagination="false" :bordered="{ cell: true }">
          <template #empty><div class="empty-text">没有失败的分块</div></template>
          <template #columns>
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
import { computed, onMounted, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  cancelKlineBackfill,
  getKlineBackfill,
  listKlineBackfills,
  retryKlineBackfill,
  type BackfillJobFilter,
  type KlineBackfillChunk,
  type KlineBackfillJob
} from "@/api/collector/runtime-status";
import { createLatestRequestGuard } from "@/utils/latest-request";
import {
  backfillCancelable,
  backfillRetryable,
  jobKindText,
  jobRangeText,
  jobScopeText,
  jobSegments,
  jobSourceText,
  jobStatusInfo,
  localTimeText,
  utcText
} from "./runtime-status-model";

type Segment = "attention" | "manual" | "auto" | "all";

const props = defineProps<{ spaceId: string }>();
const emit = defineEmits<{
  (event: "check"): void;
  (event: "backfill"): void;
  (event: "changed"): void;
}>();

const guard = createLatestRequestGuard();
const segment = ref<Segment>("attention");
const jobs = ref<KlineBackfillJob[]>([]);
const loading = ref(false);
const page = ref(1);
const pageSize = 20;
const total = ref(0);
const detail = ref<{ job?: KlineBackfillJob; failed_chunks?: KlineBackfillChunk[] } | null>(null);
const acting = ref(false);

const failedRows = computed(() =>
  (detail.value?.failed_chunks || []).map((item, index) => ({ ...item, key: `${item.subject_id}|${item.from}|${index}` }))
);

function filterOf(value: Segment): BackfillJobFilter {
  if (value === "attention") return { attention: true };
  if (value === "manual" || value === "auto") return { source: value };
  return {};
}

async function reload() {
  const spaceId = props.spaceId;
  if (!spaceId) return;
  const request = guard.begin();
  loading.value = true;
  try {
    const data = await listKlineBackfills(spaceId, filterOf(segment.value), page.value, pageSize);
    if (!request.isLatest() || spaceId !== props.spaceId) return;
    jobs.value = data.items || [];
    total.value = Number(data.page?.total || 0);
  } catch (error) {
    if (request.isLatest()) console.error("获取补采作业失败:", error);
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function onSegmentChange() {
  page.value = 1;
  void reload();
}

function onPageChange(value: number) {
  page.value = value;
  void reload();
}

async function openDetail(record: KlineBackfillJob) {
  try {
    detail.value = await getKlineBackfill(props.spaceId, record.job_id || "");
  } catch (error) {
    console.error("获取补采作业详情失败:", error);
    Message.error(error instanceof Error ? error.message : "获取补采作业详情失败");
  }
}

async function act(action: () => Promise<unknown>, success: string) {
  const jobId = detail.value?.job?.job_id || "";
  acting.value = true;
  try {
    await action();
    Message.success(success);
    detail.value = await getKlineBackfill(props.spaceId, jobId);
    emit("changed");
    void reload();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "操作失败");
  } finally {
    acting.value = false;
  }
}

const onRetry = () => act(() => retryKlineBackfill(props.spaceId, detail.value?.job?.job_id || ""), "已重新排队失败分块");
const onCancel = () => act(() => cancelKlineBackfill(props.spaceId, detail.value?.job?.job_id || ""), "已取消");

/** 新建作业后切到“需关注”第一页，能直接看到它。 */
function showAttention() {
  segment.value = "attention";
  page.value = 1;
  void reload();
}

watch(
  () => props.spaceId,
  () => {
    guard.invalidate();
    jobs.value = [];
    total.value = 0;
    page.value = 1;
    detail.value = null;
    void reload();
  }
);
onMounted(reload);

defineExpose({ reload, showAttention });
</script>

<style scoped>
.pane-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-toolbar-table);
}
.clickable-table :deep(.arco-table-tr) {
  cursor: pointer;
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
.muted,
.retention-hint {
  color: var(--color-text-3);
  font-size: 12px;
}
.retention-hint {
  margin-top: var(--moox-space-2);
}
.mono-text {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
}
.ellipsis {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.danger-text {
  color: rgb(var(--danger-6));
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
</style>

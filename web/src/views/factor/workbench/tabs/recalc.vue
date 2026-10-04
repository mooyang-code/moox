<template>
  <div v-if="set" class="recalc-tab">
    <a-alert v-if="set.status !== 'enabled'" type="warning" show-icon class="recalc-tab__tip"
      >因子集未启用，无法提交补算；已有任务仍可查看。</a-alert
    >
    <a-form class="recalc-form" :model="form" layout="vertical">
      <a-form-item label="因子">
        <a-select v-model="form.factor_ids" multiple allow-clear placeholder="留空时补算该因子集的全部已启用因子">
          <a-option v-for="factor in enabledFactors" :key="factor.factor_id" :value="factor.factor_id">
            {{ factor.name }}（{{ factor.factor_id }}）
          </a-option>
        </a-select>
      </a-form-item>
      <a-form-item label="对象">
        <a-input-tag v-model="form.subjects" allow-clear placeholder="留空时使用因子集对象范围" />
      </a-form-item>
      <a-form-item label="开始时间" required>
        <a-date-picker v-model="form.start_time" show-time value-format="timestamp" format="YYYY-MM-DD HH:mm:ss" />
      </a-form-item>
      <a-form-item label="结束时间（不含）" required>
        <a-date-picker v-model="form.end_time" show-time value-format="timestamp" format="YYYY-MM-DD HH:mm:ss" />
      </a-form-item>
      <a-form-item label="请求 ID" extra="同一请求 ID 重复提交是幂等的">
        <a-input v-model="form.request_id" allow-clear placeholder="留空自动生成" />
      </a-form-item>
      <a-form-item class="recalc-form__submit">
        <a-button type="primary" status="success" :loading="submitting" :disabled="set.status !== 'enabled'" @click="submit"
          >提交补算</a-button
        >
      </a-form-item>
    </a-form>

    <div class="recalc-tab__bar">
      <h3>补算任务</h3>
      <a-button size="small" @click="load">
        <template #icon><icon-refresh /></template>
        刷新
      </a-button>
    </div>
    <a-table
      row-key="job_id"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading"
      :data="jobs"
      :pagination="pagination"
      :row-class="rowClass"
      :scroll="{ x: 'max-content' }"
      @page-change="onPageChange"
      @page-size-change="onPageSizeChange"
    >
      <template #empty><a-empty description="该因子集还没有补算任务" /></template>
      <template #columns>
        <a-table-column title="来源" :width="100">
          <template #cell="{ record }">{{ jobSourceLabel(record) }}</template>
        </a-table-column>
        <a-table-column title="任务 ID" data-index="job_id" :width="230" :ellipsis="true" :tooltip="true" />
        <a-table-column title="因子" :width="180" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">{{ record.factor_ids.join(", ") || "全部已启用因子" }}</template>
        </a-table-column>
        <a-table-column title="状态" :width="170">
          <template #cell="{ record }">
            <a-tag size="small" :color="jobStatusTag(record.status).color">{{ jobStatusTag(record.status).label }}</a-tag>
            <a-tooltip v-if="note(record).degraded" :content="note(record).degraded">
              <a-tag size="small" color="orange">部分降级</a-tag>
            </a-tooltip>
          </template>
        </a-table-column>
        <a-table-column title="进度" :width="200">
          <template #cell="{ record }">
            <a-progress :percent="jobProgress(record) / 100" :status="record.status === 'failed' ? 'danger' : undefined" />
          </template>
        </a-table-column>
        <a-table-column title="处理至" :width="180">
          <template #cell="{ record }">{{ formatTime(record.progress_time) }}</template>
        </a-table-column>
        <a-table-column title="对象" :width="180" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">{{ record.subjects.join(", ") || "因子集范围" }}</template>
        </a-table-column>
        <a-table-column title="错误" :width="260" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">{{ note(record).error || "-" }}</template>
        </a-table-column>
        <a-table-column title="创建时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.created_at) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="90" align="center" fixed="right">
          <template #cell="{ record }">
            <a-popconfirm content="取消该补算任务？" @ok="cancel(record)">
              <a-button size="mini" type="text" status="danger" :disabled="!isActiveJob(record)">取消</a-button>
            </a-popconfirm>
          </template>
        </a-table-column>
      </template>
    </a-table>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { cancelRecalcJob, listRecalcJobs, recalcFactors } from "@/api/factor";
import type { FactorDef, FactorSet, RecalcJob } from "@/api/factor/types";
import { usePolling } from "@/hooks/usePolling";
import { useFactorStore } from "@/store/modules/factor";
import { applyPageResult, defaultPagination, formatTime } from "@/views/data/shared/metadata-utils";
import { RequestGate } from "@/utils/request-gate";
import { freqSeconds } from "../health";
import { isActiveJob, jobProgress, jobSourceLabel, jobStatusTag, splitJobNote } from "../status";

defineOptions({ name: "FactorRecalcTab" });

const DEFAULT_RANGE_PERIODS = 100;

const store = useFactorStore();
const route = useRoute();
const set = computed<FactorSet | undefined>(() => store.current?.factor_set);
const enabledFactors = computed<FactorDef[]>(() =>
  ((store.current?.factors || []) as FactorDef[]).filter(factor => factor.status === "enabled")
);
const highlightJob = computed(() => String(route.query.job || ""));

const jobs = ref<RecalcJob[]>([]);
const loading = ref(false);
const submitting = ref(false);
const pagination = reactive(defaultPagination());
const gate = new RequestGate();
const form = reactive({
  factor_ids: [] as string[],
  subjects: [] as string[],
  start_time: undefined as number | undefined,
  end_time: undefined as number | undefined,
  request_id: ""
});

const note = (job: RecalcJob) => splitJobNote(job);
const rowClass = (job: RecalcJob) => (job.job_id === highlightJob.value ? "recalc-row--highlight" : "");

async function load(options: { silent?: boolean } = {}) {
  const current = set.value;
  const token = gate.next();
  if (!current) {
    jobs.value = [];
    return;
  }
  if (!options.silent) loading.value = true;
  try {
    const rsp = await listRecalcJobs({ set_id: current.set_id, page: { page: pagination.current, size: pagination.pageSize } });
    if (!gate.isCurrent(token)) return;
    jobs.value = rsp.jobs || [];
    applyPageResult(pagination, rsp.page_result);
  } catch (error) {
    if (gate.isCurrent(token) && !options.silent) Message.error(error instanceof Error ? error.message : "补算任务加载失败");
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

function resetForm() {
  form.factor_ids = [];
  form.subjects = [];
  form.request_id = "";
  const interval = freqSeconds(set.value?.freq || "") * 1000;
  if (interval > 0) {
    const end = Math.floor(Date.now() / interval) * interval;
    form.end_time = end;
    form.start_time = end - interval * DEFAULT_RANGE_PERIODS;
  } else {
    form.start_time = undefined;
    form.end_time = undefined;
  }
}

async function submit() {
  const current = set.value;
  if (!current || form.start_time === undefined || form.end_time === undefined) {
    Message.warning("请选择补算时间范围");
    return;
  }
  if (form.end_time <= form.start_time) {
    Message.warning("结束时间必须晚于开始时间");
    return;
  }
  submitting.value = true;
  try {
    await recalcFactors({
      set_id: current.set_id,
      factor_ids: [...form.factor_ids],
      subjects: [...form.subjects],
      start_time: new Date(form.start_time).toISOString(),
      end_time: new Date(form.end_time).toISOString(),
      request_id: form.request_id.trim() || `factor-recalc-${Date.now()}`
    });
    Message.success("补算任务已受理");
    pagination.current = 1;
    await load();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "补算任务提交失败");
  } finally {
    submitting.value = false;
  }
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

function onPageChange(page: number) {
  pagination.current = page;
  load();
}

function onPageSizeChange(size: number) {
  pagination.current = 1;
  pagination.pageSize = size;
  load();
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
    resetForm();
    load();
  },
  { immediate: true }
);
</script>

<style scoped>
.recalc-form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 var(--moox-space-4);
  max-width: 960px;
  margin-bottom: var(--moox-space-4);
}

.recalc-form__submit {
  align-self: end;
}

.recalc-tab__tip {
  margin-bottom: var(--moox-space-3);
}

.recalc-tab__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
}

.recalc-tab__bar h3 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}

:deep(.recalc-row--highlight td) {
  background: var(--color-primary-light-1) !important;
}

@media (max-width: 700px) {
  .recalc-form {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>

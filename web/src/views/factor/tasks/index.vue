<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>因子补算</h2>
        <a-space>
          <a-button @click="refreshJobs">刷新任务</a-button>
          <a-tag size="small" :color="engineStatus.consumer_running ? 'green' : 'gray'">
            {{ engineStatus.consumer_running ? "实时消费运行中" : "实时消费停止" }}
          </a-tag>
        </a-space>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <a-alert v-else-if="!availableSets.length" type="info" show-icon>当前空间没有因子集</a-alert>

      <template v-else>
        <a-form class="recalc-form" :model="form" layout="vertical">
          <a-form-item label="因子集" required>
            <a-select v-model="form.set_id" placeholder="选择因子集" @change="setChanged">
              <a-option v-for="set in availableSets" :key="set.set_id" :value="set.set_id">
                {{ set.source_dataset_id }} · {{ set.freq }}（{{ set.set_id }}）
              </a-option>
            </a-select>
          </a-form-item>
          <a-form-item label="因子">
            <a-select v-model="form.factor_ids" multiple allow-clear placeholder="留空时补算该因子集的全部启用因子">
              <a-option v-for="factor in selectedFactors" :key="factor.factor_id" :value="factor.factor_id">
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
          <a-form-item label="请求 ID">
            <a-input v-model="form.request_id" allow-clear placeholder="留空自动生成" />
          </a-form-item>
          <a-form-item class="submit-item">
            <a-button type="primary" status="success" :loading="submitting" @click="submit">提交补算</a-button>
          </a-form-item>
        </a-form>

        <a-empty v-if="!visibleJobs.length" description="尚未提交补算任务" />
        <a-table v-else row-key="job_id" size="small" :bordered="{ cell: true }" :data="visibleJobs" :pagination="false" :scroll="{ x: 'max-content' }">
          <template #columns>
            <a-table-column title="任务 ID" data-index="job_id" :width="190" />
            <a-table-column title="请求 ID" data-index="request_id" :width="180" />
            <a-table-column title="状态" :width="120">
              <template #cell="{ record }"><a-tag size="small" :color="jobStatusColor(record.status)">{{ jobStatusLabel(record.status) }}</a-tag></template>
            </a-table-column>
            <a-table-column title="进度" :width="220">
              <template #cell="{ record }"><a-progress :percent="progress(record)" :status="record.status === 'failed' ? 'danger' : undefined" /></template>
            </a-table-column>
            <a-table-column title="处理至" :width="190">
              <template #cell="{ record }">{{ formatTime(record.progress_time) }}</template>
            </a-table-column>
            <a-table-column title="对象" :width="200" :ellipsis="true" :tooltip="true">
              <template #cell="{ record }">{{ record.subjects.join(", ") || "因子集范围" }}</template>
            </a-table-column>
            <a-table-column title="错误" :width="260" :ellipsis="true" :tooltip="true">
              <template #cell="{ record }">{{ record.error || "-" }}</template>
            </a-table-column>
            <a-table-column title="操作" :width="100" align="center" :fixed="'right'">
              <template #cell="{ record }">
                <a-popconfirm content="取消该补算任务？" @ok="cancel(record)">
                  <a-button size="mini" type="text" status="danger" :disabled="!isActive(record.status)">取消</a-button>
                </a-popconfirm>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { cancelRecalcJob, getFactorStatus, getRecalcJob, listFactorSets, recalcFactors } from "@/api/factor";
import type { EngineStatus, FactorSetInfo, RecalcJob } from "@/api/factor/types";
import { useSpaceStore } from "@/store/modules/space";
import { formatTime, statusLabel } from "@/views/data/shared/metadata-utils";

defineOptions({ name: "FactorTasks" });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const setInfos = ref<FactorSetInfo[]>([]);
const jobs = ref<RecalcJob[]>([]);
const submitting = ref(false);
const engineStatus = ref<EngineStatus>({
  ret_info: { code: 0, msg: "" },
  consumer_running: false,
  python_workers: 0,
  python_busy: 0,
  lanes: [],
  recent_runs: []
});
const form = reactive({
  set_id: "",
  factor_ids: [] as string[],
  subjects: [] as string[],
  start_time: undefined as number | undefined,
  end_time: undefined as number | undefined,
  request_id: ""
});
let pollTimer: number | undefined;
let setLoadSequence = 0;
const availableSets = computed(() => setInfos.value.map(item => item.factor_set).filter(set => set?.space_id === selectedSpaceId.value && set.status === "enabled"));
const selectedSet = computed(() => availableSets.value.find(set => set.set_id === form.set_id));
const selectedFactors = computed(() => setInfos.value.find(item => item.factor_set.set_id === form.set_id)?.factors.filter(factor => factor.status === "enabled") || []);
const visibleJobs = computed(() => jobs.value.filter(job => job.set_id === form.set_id));

async function loadSets() {
  const spaceId = selectedSpaceId.value;
  const sequence = ++setLoadSequence;
  if (!spaceId) {
    setInfos.value = [];
    form.set_id = "";
    form.factor_ids = [];
    form.subjects = [];
    form.start_time = undefined;
    form.end_time = undefined;
    return;
  }
  try {
    const items: FactorSetInfo[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listFactorSets({ page: { page, size: 500 } });
      items.push(...(rsp.factor_sets || []));
      if (!rsp.page_result?.has_more || !(rsp.factor_sets || []).length) break;
    }
    if (sequence !== setLoadSequence || selectedSpaceId.value !== spaceId) return;
    setInfos.value = items.filter(item => item.factor_set?.space_id === spaceId);
    const previousSetId = form.set_id;
    if (!availableSets.value.some(set => set.set_id === form.set_id)) {
      form.set_id = availableSets.value[0]?.set_id || "";
    }
    if (form.set_id !== previousSetId && selectedSet.value) setChanged();
  } catch (error) {
    if (sequence === setLoadSequence) Message.error(error instanceof Error ? error.message : "因子集加载失败");
  }
}

async function loadStatus() {
  try {
    engineStatus.value = await getFactorStatus();
  } catch {
    engineStatus.value = { ret_info: { code: 0, msg: "" }, consumer_running: false, python_workers: 0, python_busy: 0, lanes: [], recent_runs: [] };
  }
}

function defaultRange(freq: string) {
  const match = freq.trim().match(/^(\d+)(m|h|d)$/i);
  if (!match) return;
  const unitMs = match[2].toLowerCase() === "m" ? 60_000 : match[2].toLowerCase() === "h" ? 3_600_000 : 86_400_000;
  const intervalMs = Number(match[1]) * unitMs;
  if (!Number.isFinite(intervalMs) || intervalMs <= 0) return;
  const end = Math.floor(Date.now() / intervalMs) * intervalMs;
  form.end_time = end;
  form.start_time = end - intervalMs * 100;
}

function setChanged() {
  form.factor_ids = [];
  form.subjects = [];
  const freq = selectedSet.value?.freq;
  if (freq) defaultRange(freq);
}

function requestID() {
  return form.request_id.trim() || `factor-recalc-${Date.now()}`;
}

async function submit() {
  if (!selectedSet.value || form.start_time === undefined || form.end_time === undefined) {
    Message.warning("请选择因子集和补算时间范围");
    return;
  }
  if (form.end_time <= form.start_time) {
    Message.warning("结束时间必须晚于开始时间");
    return;
  }
  submitting.value = true;
  try {
    const reqId = requestID();
    const job = await recalcFactors({
      set_id: form.set_id,
      factor_ids: [...form.factor_ids],
      subjects: [...form.subjects],
      start_time: new Date(form.start_time).toISOString(),
      end_time: new Date(form.end_time).toISOString(),
      request_id: reqId
    });
    jobs.value = [job, ...jobs.value.filter(item => item.job_id !== job.job_id)];
    Message.success("补算任务已受理");
    await pollJobs();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "补算任务提交失败");
  } finally {
    submitting.value = false;
  }
}

function isActive(status: string) {
  return status === "accepted" || status === "running";
}

async function cancel(job: RecalcJob) {
  try {
    const updated = await cancelRecalcJob(job.job_id);
    replaceJob(updated);
    Message.success("补算任务已取消");
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "取消补算任务失败");
  }
}

function replaceJob(job: RecalcJob) {
  const index = jobs.value.findIndex(item => item.job_id === job.job_id);
  if (index < 0) jobs.value.unshift(job);
  else jobs.value.splice(index, 1, job);
}

async function pollJobs() {
  const activeJobs = jobs.value.filter(job => isActive(job.status));
  await Promise.all(activeJobs.map(async job => {
    try {
      replaceJob(await getRecalcJob(job.job_id));
    } catch {
      // Keep the last durable snapshot visible while a transient status read is unavailable.
    }
  }));
}

async function refreshJobs() {
  await Promise.all([pollJobs(), loadStatus()]);
}

function progress(job: RecalcJob) {
  if (job.status === "succeeded") return 100;
  const start = Date.parse(job.start_time);
  const end = Date.parse(job.end_time);
  const current = Date.parse(job.progress_time);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start || !Number.isFinite(current)) return 0;
  return Math.max(0, Math.min(100, Math.floor(((current - start) / (end - start)) * 100)));
}

function jobStatusLabel(status: string) {
  return ({ accepted: "已受理", running: "执行中", succeeded: "已完成", failed: "失败", cancelled: "已取消" } as Record<string, string>)[status] || statusLabel(status);
}

function jobStatusColor(status: string) {
  if (status === "succeeded") return "green";
  if (status === "running") return "blue";
  if (status === "failed") return "red";
  if (status === "cancelled") return "gray";
  return "orange";
}

watch(selectedSpaceId, () => loadSets());
onMounted(() => {
  loadSets();
  loadStatus();
  pollTimer = window.setInterval(() => {
    pollJobs();
    loadStatus();
  }, 3000);
});
onBeforeUnmount(() => {
  if (pollTimer !== undefined) window.clearInterval(pollTimer);
});
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-3);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.recalc-form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 var(--moox-space-4);
  max-width: 960px;
  margin-bottom: var(--moox-space-4);
}

.submit-item { align-self: end; }

@media (max-width: 700px) {
  .recalc-form { grid-template-columns: minmax(0, 1fr); }
}
</style>

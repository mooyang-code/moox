<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>计算任务及补算</h2>
        <a-space wrap>
          <a-button @click="loadStatus">刷新状态</a-button>
        </a-space>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <a-descriptions v-else :column="{ xs: 1, sm: 3 }" bordered size="small" class="engine-status">
        <a-descriptions-item label="引擎">{{ engineStatus.engine_id || "离线" }}</a-descriptions-item>
        <a-descriptions-item label="desired">{{ engineStatus.desired_revision ?? "-" }}</a-descriptions-item>
        <a-descriptions-item label="applied">{{ engineStatus.applied_revision ?? "-" }}</a-descriptions-item>
        <a-descriptions-item label="执行中">{{ engineStatus.active_tasks }}</a-descriptions-item>
        <a-descriptions-item label="等待中">{{ engineStatus.pending_tasks }}</a-descriptions-item>
        <a-descriptions-item label="Workers">{{ engineStatus.python_workers }}</a-descriptions-item>
      </a-descriptions>

      <a-form class="recalc-form" :model="form" auto-label-width>
        <a-form-item label="请求 ID" required>
          <a-input v-model="form.request_id" placeholder="请求 ID" />
        </a-form-item>
        <a-form-item label="输入数据集" required>
          <a-input v-model="form.input_dataset_id" placeholder="输入数据集" />
        </a-form-item>
        <a-form-item label="输出数据集">
          <a-input v-model="form.output_dataset_id" placeholder="输出数据集（可选，默认绑定输出）" />
        </a-form-item>
        <a-form-item label="对象" required>
          <a-input v-model="form.subject_id" placeholder="对象" />
        </a-form-item>
        <a-form-item label="频率" required>
          <a-input v-model="form.freq" placeholder="频率" />
        </a-form-item>
        <a-form-item label="开始时间" required>
          <a-input v-model="form.start_time" placeholder="开始时间" />
        </a-form-item>
        <a-form-item label="结束时间" required>
          <a-input v-model="form.end_time" placeholder="结束时间" />
        </a-form-item>
        <a-form-item>
          <a-button type="primary" :loading="submitting" @click="submit">提交补算</a-button>
        </a-form-item>
      </a-form>

      <a-empty v-if="!jobs.length" description="尚未受理补算任务" />
      <a-table v-else row-key="job_id" size="small" :bordered="{ cell: true }" :data="jobs" :pagination="false">
        <template #columns>
          <a-table-column title="任务 ID" data-index="job_id" :width="180" />
          <a-table-column title="请求 ID" data-index="request_id" :width="160" />
          <a-table-column title="状态" data-index="status" :width="120" />
          <a-table-column title="失败分类" data-index="failure_class" :width="140" />
        </template>
      </a-table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { getEngineStatus, getRecalcJob, recalcFactor } from "@/api/factor";
import type { EngineStatus, RecalcJob } from "@/api/factor/types";
import { useSpaceStore } from "@/store/modules/space";

defineOptions({ name: "FactorTasks" });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const submitting = ref(false);
const jobs = ref<RecalcJob[]>([]);
const engineStatus = ref<EngineStatus>({
  ret_info: { code: 0, msg: "" },
  python_workers: 0,
  active_tasks: 0,
  pending_tasks: 0
});
const form = reactive({
  request_id: "",
  input_dataset_id: "",
  output_dataset_id: "",
  subject_id: "",
  freq: "",
  start_time: "",
  end_time: ""
});

async function loadStatus() {
  try {
    engineStatus.value = await getEngineStatus();
  } catch {
    engineStatus.value = {
      ret_info: { code: 0, msg: "" },
      python_workers: 0,
      active_tasks: 0,
      pending_tasks: 0
    };
  }
}

async function submit() {
  const spaceId = spaceStore.requireSpaceId();
  if (!form.request_id || !form.input_dataset_id || !form.subject_id || !form.freq || !form.start_time || !form.end_time) {
    Message.warning("请补全请求 ID、输入数据集、对象、频率和时间范围");
    return;
  }
  submitting.value = true;
  try {
    const rsp = await recalcFactor({
      space_id: spaceId,
      request_id: form.request_id,
      input_dataset_id: form.input_dataset_id,
      output_dataset_id: form.output_dataset_id || undefined,
      subject_id: form.subject_id,
      freq: form.freq,
      start_time: form.start_time,
      end_time: form.end_time
    });
    const job = await getRecalcJob(rsp.job_id).catch(() => ({
      job_id: rsp.job_id,
      request_id: form.request_id,
      status: rsp.status
    }));
    jobs.value = [job, ...jobs.value.filter(item => item.job_id !== job.job_id)];
    Message.success("补算已受理");
    await loadStatus();
  } finally {
    submitting.value = false;
  }
}

watch(selectedSpaceId, loadStatus);
onMounted(loadStatus);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-3);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.engine-status {
  margin-bottom: var(--moox-space-3);
}

.recalc-form {
  max-width: 640px;
  margin-bottom: var(--moox-space-3);
}
</style>
